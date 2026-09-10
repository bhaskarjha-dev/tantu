package pairing

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func createTCPPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer ln.Close()

	ch := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		ch <- c
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	server := <-ch
	return server, client
}

func TestFramedConn(t *testing.T) {
	c1, c2 := createTCPPair(t)
	defer c1.Close()
	defer c2.Close()

	fc1 := NewFramedConn(c1)
	fc2 := NewFramedConn(c2)

	payload := map[string]string{"msg": "hello in-band"}
	errCh := make(chan error, 1)
	go func() {
		errCh <- fc1.Send("test_msg", payload)
	}()

	env, err := fc2.Receive()
	if err != nil {
		t.Fatalf("fc2.Receive failed: %v", err)
	}
	if env.Type != "test_msg" {
		t.Errorf("expected type test_msg, got %s", env.Type)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("fc1.Send failed: %v", err)
	}
}

func TestInBandPairing_Success(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	storeA, err := NewPeerStore(dirA)
	if err != nil {
		t.Fatalf("storeA init: %v", err)
	}
	storeB, err := NewPeerStore(dirB)
	if err != nil {
		t.Fatalf("storeB init: %v", err)
	}

	pA, pB := createTCPPair(t)
	defer pA.Close()
	defer pB.Close()

	connA := NewFramedConn(pA)
	connB := NewFramedConn(pB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type resA struct {
		res *PairResult
		err error
	}
	chA := make(chan resA, 1)

	go func() {
		r, err := HandleInboundPairing(ctx, connA, nil, storeA, func(peerSAS, localSAS string) bool {
			return true
		})
		chA <- resA{res: r, err: err}
	}()

	rB, errB := PairResponderInBand(ctx, connB, storeB, func(peerSAS, localSAS string) bool {
		return true
	})
	if errB != nil {
		t.Fatalf("responder pairing failed: %v", errB)
	}

	resAVal := <-chA
	if resAVal.err != nil {
		t.Fatalf("initiator pairing failed: %v", resAVal.err)
	}
	rA := resAVal.res

	if !rA.Accepted || !rB.Accepted {
		t.Fatalf("expected both to accept: rA=%+v, rB=%+v", rA, rB)
	}
	if rA.PeerSAS != rB.LocalSAS || rA.LocalSAS != rB.PeerSAS {
		t.Errorf("SAS mismatch: rA(peer=%s, local=%s) vs rB(peer=%s, local=%s)",
			rA.PeerSAS, rA.LocalSAS, rB.PeerSAS, rB.LocalSAS)
	}

	peersA := storeA.ListPeers()
	if len(peersA) != 1 {
		t.Fatalf("expected 1 peer in storeA, got %d", len(peersA))
	}
	if !strings.HasSuffix(peersA[0].Address, ":"+DefaultPairingPort) {
		t.Errorf("expected normalized port %s in stored address, got %s", DefaultPairingPort, peersA[0].Address)
	}

	peersB := storeB.ListPeers()
	if len(peersB) != 1 {
		t.Fatalf("expected 1 peer in storeB, got %d", len(peersB))
	}
	if !strings.HasSuffix(peersB[0].Address, ":"+DefaultPairingPort) {
		t.Errorf("expected normalized port %s in stored address, got %s", DefaultPairingPort, peersB[0].Address)
	}
}

func TestInBandPairing_Reject(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	storeA, _ := NewPeerStore(dirA)
	storeB, _ := NewPeerStore(dirB)

	pA, pB := createTCPPair(t)
	defer pA.Close()
	defer pB.Close()

	connA := NewFramedConn(pA)
	connB := NewFramedConn(pB)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	chA := make(chan *PairResult, 1)
	go func() {
		r, _ := HandleInboundPairing(ctx, connA, nil, storeA, func(peerSAS, localSAS string) bool {
			return false // Initiator rejects!
		})
		chA <- r
	}()

	rB, _ := PairResponderInBand(ctx, connB, storeB, func(peerSAS, localSAS string) bool {
		return true
	})

	rA := <-chA
	if rA == nil || rB == nil {
		t.Fatalf("expected non-nil results: rA=%v, rB=%v", rA, rB)
	}
	if rA.Accepted || rB.Accepted {
		t.Errorf("expected rejection, got rA.Accepted=%v, rB.Accepted=%v", rA.Accepted, rB.Accepted)
	}

	if len(storeA.ListPeers()) != 0 {
		t.Errorf("expected 0 peers in storeA, got %d", len(storeA.ListPeers()))
	}
	if len(storeB.ListPeers()) != 0 {
		t.Errorf("expected 0 peers in storeB, got %d", len(storeB.ListPeers()))
	}
}

func TestDialInBandPairing_ToPairInitiator(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	storeA, err := NewPeerStore(dirA)
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := NewPeerStore(dirB)
	if err != nil {
		t.Fatal(err)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	initDone := make(chan struct{})
	var initRes *PairResult
	var initErr error

	go func() {
		defer close(initDone)
		initRes, initErr = PairInitiatorWithListener(storeA, l, func(peerSAS, localSAS string) bool {
			return true
		})
	}()

	respRes, respErr := DialInBandPairing(l.Addr().String(), storeB, func(peerSAS, localSAS string) bool {
		return true
	})

	<-initDone

	if initErr != nil {
		t.Fatalf("PairInitiator error: %v", initErr)
	}
	if respErr != nil {
		t.Fatalf("DialInBandPairing error: %v", respErr)
	}
	if !initRes.Accepted || !respRes.Accepted {
		t.Fatalf("expected both accepted: init=%v, resp=%v", initRes.Accepted, respRes.Accepted)
	}
	if initRes.PeerSAS != respRes.LocalSAS || initRes.LocalSAS != respRes.PeerSAS {
		t.Errorf("SAS mismatch: init(peer=%s, local=%s), resp(peer=%s, local=%s)",
			initRes.PeerSAS, initRes.LocalSAS, respRes.PeerSAS, respRes.LocalSAS)
	}
}

