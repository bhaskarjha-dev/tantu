package hub

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/bhaskarjha-dev/tantu/internal/drop"
	"github.com/bhaskarjha-dev/tantu/internal/transport"
)

// Gates for in-flight upload progress.
//
// The plan's budget is "active upload progress, at least every 250ms", and
// KNOWN-LIMITATIONS 2.5 recorded why it could not be enforced: the operation
// ledger had no in-flight state, because the sender discarded the only honest
// mid-flight signal it had -- its own running byte count.
//
// What is produced here, and what is not:
//
//   - Produced: a real drop.SendDrop to a real drop.ReceiveDrop over a real
//     transport, with the sender's sends throttled so the transfer takes
//     seconds, asserting the progress reports' cadence and totals.
//   - Produced: the Hub's own ledger, driven through the same entry points the
//     two dashboard send handlers use, asserting an in-flight record advances,
//     does not append per chunk, and stops advancing once finished.
//   - Not produced: the dashboard-visible cadence end to end. That needs a
//     transfer slow enough to sample from a browser, which needs a slow reader
//     the test cannot install without a seam in production code. A structural
//     check covers the property that would otherwise be broken by implementing
//     this the obvious way -- persisting the ledger on every tick.

// budgetUploadProgress is the plan's number: "active upload progress, at least
// every 250ms".
const budgetUploadProgress = 250 * time.Millisecond

// throttledSendConn delays every data message the sender writes.
//
// transport.Conn is a protocol interface rather than a net.Conn, so the pacing
// happens where the sender actually spends its time: on Send. Without this a
// few MiB across loopback completes inside a single scheduling quantum, and any
// sampler sees either nothing or the final total -- the "an early snapshot is
// evidence of nothing" trap that made the rate-cap gate useless in Z78.
type throttledSendConn struct {
	transport.Conn
	delay time.Duration
}

func (c *throttledSendConn) Send(msgType string, payload any) error {
	if msgType == drop.TypeDropData {
		time.Sleep(c.delay)
	}
	return c.Conn.Send(msgType, payload)
}

// runThrottledTransfer sends payload to a real receiver over a real transport,
// with the sender's data messages throttled, and returns the progress reports
// with their timestamps.
//
// It is runDropPair with one connection wrapped, so the transfer is the same
// code path a user exercises rather than a stand-in for it.
func runThrottledTransfer(t *testing.T, payload []byte, chunk int, delay time.Duration) ([]time.Duration, error, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	var mu sync.Mutex
	var at []time.Duration
	start := time.Now()
	record := func() {
		mu.Lock()
		at = append(at, time.Since(start))
		mu.Unlock()
	}

	var recvErr, sendErr error
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		conn, err := listener.Accept()
		if err != nil {
			recvErr = err
			return
		}
		defer conn.Close()
		_, recvErr = drop.ReceiveDrop(ctx, conn, io.Discard, drop.ReceiveDropConfig{Timeout: 50 * time.Second})
	}()

	go func() {
		defer wg.Done()
		raw, err := tr.Dial(listener.Addr().String())
		if err != nil {
			sendErr = err
			return
		}
		defer raw.Close()
		conn := &throttledSendConn{Conn: raw, delay: delay}
		sendErr = drop.SendDrop(ctx, conn, drop.DropSend{
			DropID:    "drop-progress",
			Kind:      drop.DropKindFile,
			Name:      "progress.bin",
			Size:      int64(len(payload)),
			ChunkSize: chunk,
		}, bytes.NewReader(payload), drop.SendDropConfig{
			OnProgress: func(int64) { record() },
		})
	}()

	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	out := make([]time.Duration, len(at))
	copy(out, at)
	return out, sendErr, recvErr
}

// Progress must be reported during a real transfer, at least every 250ms.
//
// Asserted on the gaps *between reports* rather than on the count, because a
// bar that reaches 90% and then sits still for two seconds has not met a 250ms
// cadence however many updates it makes eventually.
func TestUploadProgressIsReportedThroughoutATransfer(t *testing.T) {
	const (
		chunk = 64 << 10
		size  = 4 << 20
		// 25ms per 64 KiB chunk stretches the transfer to about 1.6 seconds,
		// which is deliberate and was the first version's mistake. At 3ms the
		// whole transfer finished inside the 250ms budget, so the cadence
		// clause below could not fail for any implementation: a sender that
		// reported once every two megabytes would have passed it. The window has
		// to be several budgets long before "no gap exceeded the budget" is a
		// claim about the reporting rather than about the transfer's length.
		delay = 25 * time.Millisecond
	)
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i)
	}

	reports, sendErr, recvErr := runThrottledTransfer(t, payload, chunk, delay)
	if sendErr != nil {
		t.Fatalf("SendDrop: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("ReceiveDrop: %v", recvErr)
	}

	wantChunks := size / chunk
	if len(reports) < wantChunks {
		t.Errorf("a %d byte transfer at a %d byte chunk size produced %d progress reports; "+
			"there is one chunk per report, so fewer reports than chunks means the sender is "+
			"silently swallowing chunks", size, chunk, len(reports))
	}
	if len(reports) < 3 {
		t.Fatalf("only %d progress report(s) were produced for a %d byte transfer; the gate "+
			"cannot fail for the reason it exists if the sender barely reported", len(reports), size)
	}

	// The cadence, as the gap between successive reports.
	worst := time.Duration(0)
	for i := 1; i < len(reports); i++ {
		if gap := reports[i] - reports[i-1]; gap > worst {
			worst = gap
		}
	}
	if worst > budgetUploadProgress {
		t.Errorf("upload progress was reported at worst every %s, over the %s budget (%d reports "+
			"for %d chunks). A bar that reaches 90%% and then sits still has not met a 250ms "+
			"cadence however many updates it makes eventually",
			worst, budgetUploadProgress, len(reports), wantChunks)
	}
}

// The reported byte count is cumulative, monotonic, and ends at the real total.
//
// This is the property a progress bar is made of. A count that restarts on each
// chunk, or that stops short of the size, is worse than no bar: a reader watching
// it cannot tell a resume from a stall.
func TestReportedProgressIsCumulativeMonotonicAndReachesTheTotal(t *testing.T) {
	const (
		chunk = 64 << 10
		size  = 3 << 20
	)
	payload := make([]byte, size)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tr := transport.NewLoopbackTransport()
	listener, err := tr.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	var recvErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			recvErr = err
			return
		}
		defer conn.Close()
		_, recvErr = drop.ReceiveDrop(ctx, conn, io.Discard, drop.ReceiveDropConfig{Timeout: 50 * time.Second})
	}()

	conn, err := tr.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	var counts []int64
	sendErr := drop.SendDrop(ctx, conn, drop.DropSend{
		DropID:    "drop-counts",
		Kind:      drop.DropKindFile,
		Name:      "counts.bin",
		Size:      size,
		ChunkSize: chunk,
	}, bytes.NewReader(payload), drop.SendDropConfig{
		OnProgress: func(sent int64) { counts = append(counts, sent) },
	})
	<-done

	if sendErr != nil {
		t.Fatalf("SendDrop: %v", sendErr)
	}
	if recvErr != nil {
		t.Fatalf("ReceiveDrop: %v", recvErr)
	}
	if len(counts) == 0 {
		t.Fatal("no progress was reported at all")
	}

	for i, got := range counts {
		if want := int64((i + 1) * chunk); got != want {
			t.Errorf("report %d of %d is %d bytes; progress must be cumulative, so report n is "+
				"exactly n chunks. Got %v", i+1, len(counts), got, counts)
			break
		}
	}
	if final := counts[len(counts)-1]; final != size {
		t.Errorf("the last report is %d bytes, want the full %d", final, size)
	}
}

// A progress tick must advance one record rather than appending per chunk.
//
// Appending one per chunk would fill the bounded ledger with an entry per chunk
// of a single transfer and evict every real operation -- a user-visible loss,
// since the transfer history a person opens to see what happened would be gone.
func TestUploadProgressAdvancesOneRecordRatherThanAppendingPerChunk(t *testing.T) {
	h, _, cleanup := startTestHub(t)
	defer cleanup()

	id := h.beginOutboundOperation(OperationRecord{
		Kind: "file", Name: "big.bin", Size: 6 << 20, Destination: "laptop",
	})
	if id == "" {
		t.Fatal("beginOutboundOperation returned no operation id, so progress has nothing to advance")
	}

	// A whole transfer's worth of ticks, at the default chunk cadence.
	const chunk = 64 << 10
	for sent := int64(chunk); sent <= 6<<20; sent += chunk {
		h.advanceOutboundProgress(id, sent)
	}

	var inflight *OperationRecord
	for _, op := range h.outboundOps.GetAll() {
		op := op
		if op.OperationID == id {
			inflight = &op
		}
	}
	entries := len(h.outboundOps.GetAll())
	if entries != 1 {
		t.Errorf("one transfer's worth of progress produced %d ledger entries; a tick must advance "+
			"the record it started, or the bounded ledger fills with one entry per chunk and "+
			"evicts the real history", entries)
	}
	if inflight == nil {
		t.Fatalf("the in-flight record is not in the ledger (%d entries present)", entries)
	}
	if inflight.BytesSent != 6<<20 {
		t.Errorf("the in-flight record reports %d bytes sent, want %d", inflight.BytesSent, 6<<20)
	}
	if inflight.State != "" {
		t.Errorf("the in-flight record carries state %q; no state means the outcome is not yet "+
			"known, which is how a reader tells moving from finished", inflight.State)
	}
}

// A transfer is one row in the history, not two.
//
// This is the defect the first version of in-flight progress introduced, and it
// is worth a gate of its own because it is invisible in a diff: an in-flight
// record and a terminal record are both "a record", the list is sorted, and
// nothing crashes. What it looks like is every send appearing twice -- once with
// an outcome, once apparently without -- which is worse than showing no progress
// at all.
//
// It was caught by three existing gates that had been asserting one record per
// transfer since before progress existed. This asserts it at the unit that
// caused it.
func TestAnInFlightAndItsTerminalRecordAreOneRow(t *testing.T) {
	ledger := NewOperationLedger(8)

	// The in-flight record, with no operation ID of its own -- the send path
	// already has one, and minting a second is what separated them.
	inFlight := OperationRecord{
		OperationID: "op-shared", Kind: "file", Name: "big.bin", Size: 2048,
		Destination: "laptop", CreatedAt: time.Now(),
	}
	ledger.Add(inFlight)

	if !ledger.UpdateProgress("op-shared", 1024) {
		t.Fatal("the in-flight record refused a progress tick")
	}

	// The terminal record, under the same ID.
	ledger.Add(OperationRecord{
		OperationID: "op-shared", Kind: "file", Name: "big.bin", Size: 2048,
		Destination: "laptop", State: OperationStateCompleted,
		BytesSent: 2048, Verified: true,
	})

	all := ledger.GetAll()
	if len(all) != 1 {
		t.Fatalf("one transfer produced %d history rows: a user reading their transfer list "+
			"would see it happen twice, once of them apparently unverifiable", len(all))
	}
	if all[0].State != OperationStateCompleted || !all[0].Verified {
		t.Errorf("the surviving row is %+v; the terminal record must replace the in-flight one "+
			"rather than sit beside it", all[0])
	}
	if all[0].BytesSent != 2048 {
		t.Errorf("the surviving row reports %d bytes, want the verified 2048", all[0].BytesSent)
	}
}

// A finished transfer must not be walked backwards by a late progress tick.
//
// BytesSent on a completed record is a verified total. Letting a stray callback
// overwrite it restates a settled fact as something still moving -- the same
// class of defect as the cockpit calling an unreachable peer Online.
func TestAFinishedTransferCannotBeRewoundByAProgressTick(t *testing.T) {
	ledger := NewOperationLedger(8)
	ledger.Add(OperationRecord{
		OperationID: "op-1", Kind: "file", Size: 1024,
		State: OperationStateCompleted, BytesSent: 1024, Verified: true,
	})

	if ledger.UpdateProgress("op-1", 512) {
		t.Error("a completed operation accepted a progress tick; its BytesSent is a verified " +
			"total and a late callback must not restate it as still moving")
	}
	for _, op := range ledger.GetAll() {
		if op.OperationID == "op-1" && op.BytesSent != 1024 {
			t.Errorf("the completed record's byte count became %d, want the verified 1024", op.BytesSent)
		}
	}

	ledger.Add(OperationRecord{OperationID: "op-2", Kind: "file", Size: 2048})
	for _, step := range []int64{512, 1024, 1536} {
		if !ledger.UpdateProgress("op-2", step) {
			t.Fatalf("an in-flight operation refused a progress tick reporting %d bytes", step)
		}
	}
	// A tick reporting fewer bytes than already recorded is not progress.
	ledger.UpdateProgress("op-2", 100)
	for _, op := range ledger.GetAll() {
		if op.OperationID == "op-2" && op.BytesSent != 1536 {
			t.Errorf("the in-flight byte count is %d, want 1536; progress must be monotonic", op.BytesSent)
		}
	}

	if ledger.UpdateProgress("op-nope", 1) {
		t.Error("a progress tick created an operation that does not exist")
	}
	if n := len(ledger.GetAll()); n != 2 {
		t.Errorf("the ledger holds %d entries; a tick naming an unknown id must not create one", n)
	}
}
