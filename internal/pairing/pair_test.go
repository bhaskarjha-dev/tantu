package pairing

import (
	"net"
	"sync"
	"testing"
)

func TestGetOrGenerateIdentityConcurrent(t *testing.T) {
	dir := t.TempDir()
	storeA, err := NewPeerStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := NewPeerStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]*Identity, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ids[0], errs[0] = getOrGenerateIdentity(storeA)
	}()
	go func() {
		defer wg.Done()
		ids[1], errs[1] = getOrGenerateIdentity(storeB)
	}()
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("identity creation errors: %v / %v", errs[0], errs[1])
	}
	if ids[0] == nil || ids[1] == nil || ids[0].Fingerprint != ids[1].Fingerprint {
		t.Fatalf("concurrent pairing callers produced different identities: %+v / %+v", ids[0], ids[1])
	}
}

func TestPairing_Success(t *testing.T) {
	dirA := t.TempDir()
	storeA, err := NewPeerStore(dirA)
	if err != nil {
		t.Fatalf("NewPeerStore A: %v", err)
	}

	dirB := t.TempDir()
	storeB, err := NewPeerStore(dirB)
	if err != nil {
		t.Fatalf("NewPeerStore B: %v", err)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer l.Close()
	addr := l.Addr().String()

	var (
		initResult *PairResult
		respResult *PairResult
		initErr    error
		respErr    error
		wg         sync.WaitGroup
	)

	wg.Add(2)

	go func() {
		defer wg.Done()
		initResult, initErr = PairInitiatorWithListener(storeA, l, func(peerSAS, localSAS string) bool {
			if len(peerSAS) != 6 || len(localSAS) != 6 {
				t.Errorf("expected 6-char SAS codes: peer=%s, local=%s", peerSAS, localSAS)
			}
			return true
		})
	}()

	go func() {
		defer wg.Done()
		respResult, respErr = PairResponder(storeB, addr, func(peerSAS, localSAS string) bool {
			if len(peerSAS) != 6 || len(localSAS) != 6 {
				t.Errorf("expected 6-char SAS codes: peer=%s, local=%s", peerSAS, localSAS)
			}
			return true
		})
	}()

	wg.Wait()

	if initErr != nil {
		t.Fatalf("PairInitiator error: %v", initErr)
	}
	if respErr != nil {
		t.Fatalf("PairResponder error: %v", respErr)
	}

	if !initResult.Accepted {
		t.Error("expected initiator to accept")
	}
	if !respResult.Accepted {
		t.Error("expected responder to accept")
	}

	// Verify SAS cross-matching
	if initResult.PeerSAS != respResult.LocalSAS {
		t.Errorf("initiator peerSAS %s != responder localSAS %s", initResult.PeerSAS, respResult.LocalSAS)
	}
	if initResult.LocalSAS != respResult.PeerSAS {
		t.Errorf("initiator localSAS %s != responder peerSAS %s", initResult.LocalSAS, respResult.PeerSAS)
	}

	// Verify both stores have peer saved
	idA, _ := storeA.LoadIdentity()
	idB, _ := storeB.LoadIdentity()

	if !storeA.IsTrusted(idB.Fingerprint) {
		t.Errorf("storeA does not trust peer B fingerprint: %s", idB.Fingerprint)
	}
	if !storeB.IsTrusted(idA.Fingerprint) {
		t.Errorf("storeB does not trust peer A fingerprint: %s", idA.Fingerprint)
	}
}

func TestPairing_InitiatorRejects(t *testing.T) {
	dirA := t.TempDir()
	storeA, _ := NewPeerStore(dirA)
	dirB := t.TempDir()
	storeB, _ := NewPeerStore(dirB)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer l.Close()
	addr := l.Addr().String()

	var (
		initResult *PairResult
		respResult *PairResult
		initErr    error
		respErr    error
		wg         sync.WaitGroup
	)

	wg.Add(2)

	go func() {
		defer wg.Done()
		initResult, initErr = PairInitiatorWithListener(storeA, l, func(peerSAS, localSAS string) bool {
			return false // Reject!
		})
	}()

	go func() {
		defer wg.Done()
		respResult, respErr = PairResponder(storeB, addr, func(peerSAS, localSAS string) bool {
			return true // Accept
		})
	}()

	wg.Wait()

	if initErr != nil {
		t.Fatalf("initiator error: %v", initErr)
	}
	if respErr != nil {
		t.Fatalf("responder error: %v", respErr)
	}

	if initResult.Accepted {
		t.Error("expected initiator accepted to be false")
	}
	if respResult.Accepted {
		t.Error("expected responder accepted to be false")
	}

	if len(storeA.ListPeers()) != 0 {
		t.Errorf("storeA should not have saved peers, got %d", len(storeA.ListPeers()))
	}
	if len(storeB.ListPeers()) != 0 {
		t.Errorf("storeB should not have saved peers, got %d", len(storeB.ListPeers()))
	}
}

func TestPairing_ResponderRejects(t *testing.T) {
	dirA := t.TempDir()
	storeA, _ := NewPeerStore(dirA)
	dirB := t.TempDir()
	storeB, _ := NewPeerStore(dirB)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer l.Close()
	addr := l.Addr().String()

	var (
		initResult *PairResult
		respResult *PairResult
		initErr    error
		respErr    error
		wg         sync.WaitGroup
	)

	wg.Add(2)

	go func() {
		defer wg.Done()
		initResult, initErr = PairInitiatorWithListener(storeA, l, func(peerSAS, localSAS string) bool {
			return true // Accept
		})
	}()

	go func() {
		defer wg.Done()
		respResult, respErr = PairResponder(storeB, addr, func(peerSAS, localSAS string) bool {
			return false // Reject!
		})
	}()

	wg.Wait()

	if initErr != nil {
		t.Fatalf("initiator error: %v", initErr)
	}
	if respErr != nil {
		t.Fatalf("responder error: %v", respErr)
	}

	if initResult.Accepted {
		t.Error("expected initiator accepted to be false")
	}
	if respResult.Accepted {
		t.Error("expected responder accepted to be false")
	}

	if len(storeA.ListPeers()) != 0 {
		t.Errorf("storeA should not have saved peers, got %d", len(storeA.ListPeers()))
	}
	if len(storeB.ListPeers()) != 0 {
		t.Errorf("storeB should not have saved peers, got %d", len(storeB.ListPeers()))
	}
}

func TestPairing_AutoGenerateIdentity(t *testing.T) {
	dirA := t.TempDir()
	storeA, _ := NewPeerStore(dirA)
	dirB := t.TempDir()
	storeB, _ := NewPeerStore(dirB)

	if storeA.HasIdentity() || storeB.HasIdentity() {
		t.Fatal("stores should not have identity initially")
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer l.Close()
	addr := l.Addr().String()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _ = PairInitiatorWithListener(storeA, l, func(peerSAS, localSAS string) bool { return true })
	}()

	go func() {
		defer wg.Done()
		_, _ = PairResponder(storeB, addr, func(peerSAS, localSAS string) bool { return true })
	}()

	wg.Wait()

	if !storeA.HasIdentity() {
		t.Error("storeA should have generated and saved identity")
	}
	if !storeB.HasIdentity() {
		t.Error("storeB should have generated and saved identity")
	}
}
