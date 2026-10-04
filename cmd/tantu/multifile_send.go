package main

// runDirectorySend sends every file in a directory as an independent
// transfer.
//
// The two transport paths a single send already uses are reused rather than
// reimplemented: when a Hub is running, each file is delegated to it, which
// is the documented preferred path and is what gives the batch proper
// operation records in `tantu transfers`; otherwise the direct transport is
// dialled once per file, which is also how the single-file path behaves.
//
// The batch deliberately does not stop at the first failure. A directory of
// fifty files where the eleventh is unreadable should deliver the other
// forty-nine and say precisely which one did not arrive. Stopping early would
// make the user repeat a run that mostly worked.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/hub"
	"github.com/bhaskarjha-dev/tantu/internal/pairing"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// batchOptions carries the send flags a batch honours, as values rather than
// flag pointers so the runner is testable without a FlagSet.
type batchOptions struct {
	StoreDir   string
	BridgeAddr string
	PeerAddr   string
	Timeout    time.Duration
	JSONOut    bool
	Verbose    bool
	// Flatten drops the directory structure instead of preserving it. It is the
	// opt-out from the default, not the default: "send this folder" means the
	// folder, and a peer who wants the contents flat can ask for that
	// explicitly.
	Flatten bool
}

// runDirectorySend is the entry point for `tantu send <directory>`.
func runDirectorySend(root, sendKey string, storeDir, bridgeAddr, peerAddr string, timeout time.Duration, flatten bool, _ string, jsonOut, verbose bool) {
	files, expErr := expandDirectory(root, !flatten)
	if expErr != nil {
		if jsonOut {
			emitSendJSON(sendJSONResult{Status: "error", Code: expErr.Code, Message: expErr.Message, NextAction: expErr.NextAction}, sendExitUsage)
		}
		fmt.Fprintf(os.Stderr, "Error: %s\n", expErr.Message)
		if expErr.NextAction != "" {
			fmt.Fprintf(os.Stderr, "Next: %s\n", expErr.NextAction)
		}
		os.Exit(sendExitUsage)
	}

	opts := batchOptions{StoreDir: storeDir, BridgeAddr: bridgeAddr, PeerAddr: peerAddr, Timeout: timeout, JSONOut: jsonOut, Verbose: verbose, Flatten: flatten}

	var totalBytes int64
	for _, f := range files {
		totalBytes += f.Size
	}

	// In JSON mode stdout is a machine-readable contract and nothing else
	// may be written to it; progress belongs on stderr, where a caller
	// parsing stdout will never see it.
	if jsonOut {
		fmt.Fprintf(os.Stderr, "Sending %d file(s) (%s) from %s...\n", len(files), formatSize(totalBytes), root)
	} else {
		fmt.Printf("📁 Sending %d file(s) from %s (%s)\n", len(files), root, formatSize(totalBytes))
	}

	sender, closeSender := newBatchSender(opts, peerAddr)
	defer closeSender()

	result := sendBatchFiles(sender, files, root, sendKey, opts)
	// The destination is learned from the first answer, so the summary is
	// printed after the run rather than announced up front. Claiming a
	// destination before any transfer has resolved it would be a guess.
	if result.Destination == "" {
		result.Destination = sender.destination()
	}

	if jsonOut {
		emitSendBatchJSON(result, batchExitCode(result))
	}
	printBatchResult(result, opts)
	// The human path must carry the same exit code as the JSON one. A batch
	// that reported "0 of 4 delivered" and then exited 0 would be read as
	// success by any script, and would hide a total failure behind a
	// plausible-looking summary.
	os.Exit(batchExitCode(result))
}

// batchExitCode reduces a batch to one process exit code.
//
// The worst outcome in the batch wins, because a partial success that exits 0
// is the most dangerous thing this command could do: a script would treat
// 50 files as sent when 3 never arrived.
func batchExitCode(res sendBatchJSONResult) int {
	worst := sendExitOK
	for _, f := range res.Files {
		switch {
		case f.DuplicateRisk:
			// An unknown outcome is the most severe case: a retry could
			// produce a second copy.
			return sendExitDuplicateRisk
		case f.ExitCode == sendExitFailed:
			worst = sendExitFailed
		case f.ExitCode == sendExitUsage && worst == sendExitOK:
			worst = sendExitUsage
		}
	}
	return worst
}

// batchSender sends one file. Both implementations below satisfy it, so the
// batch loop has no knowledge of transports.
type batchSender interface {
	SendFile(ctx context.Context, f expandedFile, idemKey string) (sendBatchFile, error)
	destination() string
	close()
}

// newBatchSender prefers a running Hub, matching how a single send chooses its
// path, and falls back to a direct transport.
func newBatchSender(opts batchOptions, peerAddr string) (batchSender, func()) {
	hubURL := ""
	if status, ok := hub.ProbeHubWithStoreDir(opts.BridgeAddr, opts.StoreDir); ok {
		if status.WebAddr != "" {
			hubURL = status.WebAddr
		}
	}
	if hubURL != "" {
		return &hubBatchSender{webAddr: hubURL, storeDir: opts.StoreDir, timeout: opts.Timeout, peer: peerAddr}, func() {}
	}
	direct, err := newDirectBatchSender(opts, peerAddr)
	if err != nil {
		// There is no Hub and no usable direct transport. Report it as a
		// single clear failure rather than failing file by file.
		return &failingBatchSender{err: err}, func() {}
	}
	return direct, func() {}
}

// hubBatchSender delegates each file to the running Hub.
type hubBatchSender struct {
	webAddr  string
	storeDir string
	timeout  time.Duration
	// peer carries an explicit --peer through to every delegated upload. It
	// is the difference between honouring the destination the user asked for
	// and silently sending to whatever peer happens to be active: a batch that
	// ignored it would deliver 50 files to the wrong machine and report
	// success, which is the single worst outcome this command can produce.
	peer string
	// dest is recorded from the Hub's first answer so the summary names where
	// the batch actually went instead of leaving the field blank.
	dest string
}

// destination returns the batch's destination label. The Hub reports it per
// file, so it is filled in from the first successful result rather than
// guessed up front.
func (s *hubBatchSender) destination() string { return s.dest }

func (s *hubBatchSender) close() {}

func (s *hubBatchSender) SendFile(_ context.Context, f expandedFile, idemKey string) (sendBatchFile, error) {
	out := sendBatchFile{Name: f.Name, Path: f.Path, RelPath: f.RelPath, Size: f.Size}
	res, err := delegateSendWithNameFromStore(s.storeDir, s.webAddr, f.Path, "", f.Name, s.peer, s.timeout, idemKey, f.RelPath)
	if err != nil {
		he := hubFailureFields(err)
		out.Code, out.Message = he.code, he.message
		out.RetrySafe, out.DuplicateRisk = he.retrySafe, he.duplicateRisk
		out.ExitCode = sendExitForHubError(err)
		return out, err
	}
	if res != nil {
		out.OperationID = res.OperationID
		out.Verified = res.Verified
		if s.dest == "" {
			s.dest = res.Destination
		}
	}
	out.ExitCode = sendExitOK
	return out, nil
}

// directBatchSender dials the peer itself when no Hub is running.
type directBatchSender struct {
	tr          transport.Transport
	dialTarget  string
	fingerprint string
	dest        string
	timeout     time.Duration
}

func newDirectBatchSender(opts batchOptions, peerAddr string) (*directBatchSender, error) {
	store, err := openPeerStore(opts.StoreDir)
	if err != nil {
		return nil, fmt.Errorf("cannot open peer store: %w", err)
	}
	tr, target, fingerprint, err := dialTargetForPeer(store, peerAddr)
	if err != nil {
		return nil, err
	}
	return &directBatchSender{
		tr:          tr,
		dialTarget:  target,
		fingerprint: fingerprint,
		dest:        displayDestination("lan", store, peerAddr, target),
		timeout:     opts.Timeout,
	}, nil
}

func (s *directBatchSender) destination() string { return s.dest }

func (s *directBatchSender) close() {}

func (s *directBatchSender) SendFile(ctx context.Context, f expandedFile, idemKey string) (sendBatchFile, error) {
	out := sendBatchFile{Name: f.Name, Path: f.Path, RelPath: f.RelPath, Size: f.Size}
	conn, err := transport.DialPinned(s.tr, s.dialTarget, s.fingerprint)
	if err != nil {
		code, plain, _, retrySafe, duplicateRisk, _ := hub.ClassifyTransferError(err, 0, f.Size, false)
		out.Code, out.Message = code, plain
		out.RetrySafe, out.DuplicateRisk = retrySafe, duplicateRisk
		out.ExitCode = sendExitFailed
		return out, err
	}
	defer conn.Close()

	file, err := os.Open(f.Path)
	if err != nil {
		out.Code, out.Message = "input_error", fmt.Sprintf("cannot open %s: %v", f.Path, err)
		out.RetrySafe = true
		out.ExitCode = sendExitFailed
		return out, err
	}
	defer file.Close()

	dropID := drop.NewDropID()
	meta := drop.DropSend{
		DropID:   dropID,
		Kind:     drop.DropKindFile,
		Name:     f.Name,
		RelPath:  f.RelPath,
		Size:     f.Size,
		MIMEType: mimeTypeFor(f.Name),
		// A caller-supplied key applies to the whole batch, so each file
		// derives its own from it. Without that, every file would share one
		// key and the receiver would treat all but the first as conflicts.
		IdempotencyKey: batchFileKey(idemKey, dropID, f),
	}

	sendTimeout := s.timeout
	if sendTimeout <= 0 {
		sendTimeout = 5 * time.Minute
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	suppressed := false
	cfg := drop.SendDropConfig{Timeout: sendTimeout}
	cfg.OnComplete = func(c drop.DropComplete) { suppressed = c.Duplicate }
	if err := drop.SendDrop(sendCtx, conn, meta, file, cfg); err != nil {
		code, plain, _, retrySafe, duplicateRisk, _ := hub.ClassifyTransferError(err, f.Size, f.Size, false)
		out.Code, out.Message = code, plain
		out.RetrySafe, out.DuplicateRisk = retrySafe, duplicateRisk
		out.OperationID = dropID
		out.ExitCode = sendExitFailed
		if duplicateRisk {
			out.ExitCode = sendExitDuplicateRisk
		}
		return out, err
	}
	out.OperationID = dropID
	out.Verified = true
	out.Suppressed = suppressed
	out.ExitCode = sendExitOK
	return out, nil
}

// failingBatchSender reports one setup failure for every file, so a run that
// cannot connect says why once per file instead of pretending to have tried.
type failingBatchSender struct{ err error }

func (s *failingBatchSender) destination() string { return "" }
func (s *failingBatchSender) close()              {}

func (s *failingBatchSender) SendFile(_ context.Context, f expandedFile, _ string) (sendBatchFile, error) {
	return sendBatchFile{
		Name: f.Name, Path: f.Path, RelPath: f.RelPath, Size: f.Size,
		Code: "config_error", Message: s.err.Error(),
		RetrySafe: true, ExitCode: sendExitFailed,
	}, s.err
}

// hubFailureFields extracts the Hub's structured error contract.
type hubFailure struct {
	code, message, nextAction string
	retrySafe, duplicateRisk  bool
}

func hubFailureFields(err error) hubFailure {
	out := hubFailure{code: "transfer_failed", message: err.Error()}
	var he *hubError
	if errors.As(err, &he) {
		if he.Code != "" {
			out.code = he.Code
		}
		if he.PlainMessage != "" {
			out.message = he.PlainMessage
		}
		out.nextAction = he.NextAction
		out.retrySafe = he.RetrySafe
		out.duplicateRisk = he.DuplicateRisk
	}
	return out
}

// batchFileKey derives a per-file idempotency key from the batch key.
//
// This is the reason a directory can be retried safely. A single key applied
// to every file would make the receiver treat the second file as a conflicting
// redelivery of the first, because it records the key against the content it
// completed. Deriving a distinct key per file keeps the retry contract that
// `--idempotency-key` already promises.
func batchFileKey(baseKey, dropID string, f expandedFile) string {
	if baseKey == "" {
		return dropID
	}
	derived := baseKey + "-" + strings.ReplaceAll(f.Name, "/", "-")
	if !drop.ValidTombstoneKey(derived) {
		// The derived key can exceed the length bound on a deeply nested path.
		// Falling back to the per-attempt ID keeps correctness (a fresh key is
		// always valid) at the cost of duplicate suppression for that file,
		// which is the safe direction: it can duplicate, never lose.
		return dropID
	}
	return derived
}

func mimeTypeFor(name string) string {
	return mime.TypeByExtension(filepath.Ext(name))
}

// signalContext returns a context cancelled by SIGINT/SIGTERM, so a batch can
// be stopped the same way a single send is.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// dialTargetForPeer resolves the transport and dial target for a direct send,
// applying the same trust rules as a single-file send: the target must be a
// trusted paired peer, and the expected certificate fingerprint is carried
// through to the dial so a peer cannot be substituted after resolution.
func dialTargetForPeer(store *pairing.PeerStore, peerAddr string) (transport.Transport, string, string, error) {
	if store == nil {
		return nil, "", "", errors.New("cannot open peer store")
	}
	id, err := store.LoadIdentity()
	if err != nil || id == nil {
		return nil, "", "", errors.New("no local identity; run `tantu pair` first")
	}
	tlsCert, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid local TLS identity: %w", err)
	}
	resolved, err := resolvePeerForDial(store, peerAddr)
	if err != nil {
		return nil, "", "", fmt.Errorf("not a trusted paired peer: %w", err)
	}
	tr, err := transport.NewLANTransport(transport.LANTransportConfig{
		Cert:      tlsCert,
		IsTrusted: store.IsTrusted,
	})
	if err != nil {
		return nil, "", "", fmt.Errorf("cannot configure LAN transport: %w", err)
	}
	return tr, ensurePeerLANPort(resolved.Address), resolved.Fingerprint, nil
}

// sendBatchFiles runs the batch and returns the aggregate result.
func sendBatchFiles(sender batchSender, files []expandedFile, root, sendKey string, opts batchOptions) sendBatchJSONResult {
	res := sendBatchJSONResult{
		Status:      "success",
		Source:      root,
		Destination: sender.destination(),
		Total:       len(files),
		Files:       make([]sendBatchFile, 0, len(files)),
	}

	ctx, stop := signalContext()
	defer stop()

	for _, f := range files {
		if opts.Verbose || !opts.JSONOut {
			// The path inside the tree, not the bare leaf: with two files
			// called notes.txt in different folders, a list of leaf names
			// cannot tell the user which one failed.
			shown := f.Name
			if f.RelPath != "" {
				shown = f.RelPath
			}
			line := fmt.Sprintf("  → %s (%s)\n", shown, formatSize(f.Size))
			if opts.JSONOut {
				fmt.Fprint(os.Stderr, line)
			} else {
				fmt.Print(line)
			}
		}
		out, err := sender.SendFile(ctx, f, sendKey)
		if err != nil && out.Message == "" {
			out.Message = err.Error()
		}
		res.Bytes += f.Size
		res.Files = append(res.Files, out)
	}

	return aggregateBatch(res.Files, res, res.Total)
}

// aggregateBatch derives the batch status, counts, and next action from the
// per-file outcomes.
//
// It is separated from the sending loop because these are the rules that make
// a batch reportable, and they are the part that must be provably correct: a
// summary that disagrees with the per-file results is worse than no summary,
// because a caller stops reading the per-file results.
func aggregateBatch(files []sendBatchFile, res sendBatchJSONResult, total int) sendBatchJSONResult {
	res.Total = total
	res.Succeeded, res.Failed = 0, 0
	anyAmbiguous := false
	for _, f := range files {
		if f.ExitCode == sendExitOK {
			res.Succeeded++
		} else {
			res.Failed++
		}
		if f.DuplicateRisk {
			anyAmbiguous = true
		}
	}

	// The status answers "did the batch deliver what it was asked to?".
	//
	// A run where nothing is known to have arrived is "error", even when
	// every individual file reported success - that combination means the
	// outcomes are unresolved, not delivered. A run with at least one file
	// confirmed delivered but something unknown is "partial", because part of
	// it provably arrived. Only a run with every file confirmed and no
	// unresolved outcome is "success".
	switch {
	case anyAmbiguous && res.Succeeded == 0:
		res.Status = "error"
	case res.Failed == 0 && !anyAmbiguous:
		res.Status = "success"
	case res.Succeeded == 0:
		res.Status = "error"
	default:
		res.Status = "partial"
	}

	switch {
	case anyAmbiguous:
		res.NextAction = "Check the receiver inbox for the files marked with duplicate risk before retrying."
	case res.Failed == res.Total:
		// Nothing arrived, so "the rest arrived" would be a lie. The one
		// useful thing to say is why, and that retrying is safe.
		res.NextAction = "Nothing was sent. Check the destination and the peer is online, then try again."
	case res.Failed > 0:
		res.NextAction = fmt.Sprintf("Re-send the %d failed file(s) individually. The other %d arrived.", res.Failed, res.Succeeded)
	default:
		res.NextAction = ""
	}
	return res
}

// printBatchResult renders the human summary. It reports counts and names the
// failures, because a batch that says "49 of 50" without saying which one is
// not actionable.
func printBatchResult(res sendBatchJSONResult, opts batchOptions) {
	if opts.JSONOut {
		return
	}
	fmt.Println()
	if res.Failed == 0 {
		fmt.Printf("✅ Sent %d of %d file(s) (%s) to %s\n", res.Succeeded, res.Total, formatSize(res.Bytes), res.Destination)
	} else {
		fmt.Printf("⚠️  Sent %d of %d file(s); %d failed\n", res.Succeeded, res.Total, res.Failed)
	}
	if res.Failed > 0 {
		fmt.Println("\nNot delivered:")
		for _, f := range res.Files {
			if f.ExitCode == sendExitOK {
				continue
			}
			marker := "❌"
			if f.DuplicateRisk {
				marker = "⚠️ "
			}
			shown := f.Name
			if f.RelPath != "" {
				shown = f.RelPath
			}
			fmt.Printf("  %s %s — %s\n", marker, shown, f.Message)
		}
	}
	if res.NextAction != "" {
		fmt.Printf("\nNext: %s\n", res.NextAction)
	}
}
