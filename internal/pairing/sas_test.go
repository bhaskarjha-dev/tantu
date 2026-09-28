package pairing

import (
	"fmt"
	"strings"
	"testing"
)

// The word list is a security parameter: its size bounds how much entropy a
// rendered SAS can carry, and a duplicate or oddly shaped entry makes a human
// comparison unreliable.
func TestSASWordListIsSound(t *testing.T) {
	// A power-of-two list means every word is equally likely under a bit mask,
	// so the stated per-word bit count is exact rather than approximate.
	if len(SASWords)&(len(SASWords)-1) != 0 {
		t.Errorf("word list size %d is not a power of two, so words are not equiprobable", len(SASWords))
	}
	if SASWordCount != 1<<SASWordCountBits {
		t.Errorf("SASWordCount = %d but %d words were claimed at %d bits each",
			SASWordCount, SASWordCount, SASWordCountBits)
	}
	if got := SASWordTotal * SASWordCountBits; got != SASEntropyBits {
		t.Errorf("SASEntropyBits = %d, want %d", SASEntropyBits, got)
	}
	if SASEntropyBits < 40 {
		t.Errorf("a SAS carries only %d bits, which is below the 40-bit floor", SASEntropyBits)
	}

	seen := make(map[string]bool, len(SASWords))
	for _, w := range SASWords {
		if seen[w] {
			t.Errorf("duplicate word %q in the SAS word list", w)
		}
		seen[w] = true
		if len(w) < 4 || len(w) > 6 {
			t.Errorf("word %q should be 4-6 letters for a comparable code", w)
		}
		for _, r := range w {
			if r < 'a' || r > 'z' {
				t.Errorf("word %q must be lowercase ASCII letters only", w)
			}
		}
	}
}

// A SAS must be bound to the whole transcript. The old implementation was a
// static prefix of one certificate, so the same code applied to every pairing
// with that peer forever and could be pre-computed before the session even
// started.
func TestTranscriptSASIsBoundToTheSession(t *testing.T) {
	localFP := strings.Repeat("a", 64)
	peerFP := strings.Repeat("b", 64)
	localNonce := []byte("local-nonce-value-01")
	peerNonce := []byte("peer-nonce-value-001")

	base := TranscriptSAS(localFP, peerFP, localNonce, peerNonce)

	// Every component must change the result.
	variants := map[string]string{
		"different local cert": TranscriptSAS(strings.Repeat("c", 64), peerFP, localNonce, peerNonce),
		"different peer cert":  TranscriptSAS(localFP, strings.Repeat("d", 64), localNonce, peerNonce),
		"different local nonce": TranscriptSAS(localFP, peerFP,
			[]byte("local-nonce-value-02"), peerNonce),
		"different peer nonce": TranscriptSAS(localFP, peerFP, localNonce,
			[]byte("peer-nonce-value-002")),
	}
	for name, got := range variants {
		if got == base {
			t.Errorf("SAS must change with the %s; it is not bound to the transcript", name)
		}
	}

	// Swapping the roles of the two machines must NOT change the value: the
	// initiator and the responder each compute it from their own local/peer
	// view, and both screens must show the identical code.
	if swapped := TranscriptSAS(peerFP, localFP, peerNonce, localNonce); swapped != base {
		t.Error("the SAS must be identical when the initiator and responder roles are swapped")
	}
	// A peer presenting a different certificate must produce a different code.
	if other := TranscriptSAS(localFP, strings.Repeat("e", 64), localNonce, peerNonce); other == base {
		t.Error("a different peer certificate must change the SAS")
	}
}

// The same session must always render the same code, or a human could never
// compare it.
func TestTranscriptSASIsDeterministic(t *testing.T) {
	a := TranscriptSAS("aabb", "ccdd", []byte("n1"), []byte("n2"))
	b := TranscriptSAS("aabb", "ccdd", []byte("n1"), []byte("n2"))
	if a != b {
		t.Fatalf("the same transcript must render the same SAS: %q vs %q", a, b)
	}
	// Fingerprint case and surrounding whitespace are normalized, since the
	// same certificate fingerprint can be presented in either case.
	if TranscriptSAS("AABB", "CCDD", []byte("n1"), []byte("n2")) != a {
		t.Error("fingerprint case must not change the SAS")
	}
	if TranscriptSAS(" aabb ", " ccdd", []byte("n1"), []byte("n2")) != a {
		t.Error("fingerprint whitespace must not change the SAS")
	}
}

func TestTranscriptSASFormat(t *testing.T) {
	got := TranscriptSAS("aabb", "ccdd", []byte("n1"), []byte("n2"))
	parts := strings.Split(got, "-")
	if len(parts) != SASWordTotal {
		t.Fatalf("SAS %q has %d words, want %d", got, len(parts), SASWordTotal)
	}
	for _, p := range parts {
		if !strings.Contains(" "+strings.Join(SASWords[:], " ")+" ", " "+strings.ToLower(p)+" ") {
			t.Errorf("SAS word %q is not in the word list", p)
		}
		if p != strings.ToUpper(p) {
			t.Errorf("SAS word %q should be uppercase for readability", p)
		}
	}
}

// SASCode is retained for naming nodes in a list. It must never be mistaken for
// a security decision, so assert it is only ever a 24-bit-looking short label
// and is clearly distinct from the transcript SAS.
func TestSASCodeIsOnlyALabel(t *testing.T) {
	fp := "abcdef0123456789"
	if got := SASCode(fp); got != "abcdef" {
		t.Errorf("SASCode = %q, want the first 6 characters", got)
	}
	if got := SASCode("abc"); got != "abc" {
		t.Errorf("a short input must be returned unchanged, got %q", got)
	}
	// The label and the security code are different functions and must not
	// produce the same shape of output.
	label := SASCode(fp)
	security := TranscriptSAS(fp, fp, []byte("n1"), []byte("n2"))
	if strings.Contains(security, label) && len(security) == len(label) {
		t.Error("the security SAS and the display label must not be interchangeable")
	}
}

// Distinct sessions must produce distinct codes in practice, and the mapping
// must be uniform enough that no word dominates. A modulo or bit-extraction bug
// that collapsed the range would show up here as a tiny unique set.
func TestTranscriptSASVariesAcrossSessions(t *testing.T) {
	seen := map[string]bool{}
	wordUse := map[string]int{}
	localFP := strings.Repeat("a", 64)
	peerFP := strings.Repeat("b", 64)
	const sessions = 2000
	for i := 0; i < sessions; i++ {
		nonce := []byte(fmt.Sprintf("nonce-%04d", i))
		code := TranscriptSAS(localFP, peerFP, nonce, []byte("peer"))
		if seen[code] {
			t.Fatalf("duplicate SAS %q within %d sessions", code, sessions)
		}
		seen[code] = true
		for _, w := range strings.Split(code, "-") {
			wordUse[w]++
		}
	}
	// Every slot must be reachable, and no slot may be wildly over-represented
	// relative to uniform expectation (2000*6/512 ≈ 23 uses per word).
	if len(wordUse) != SASWordCount {
		t.Errorf("only %d of %d words appeared across %d sessions", len(wordUse), SASWordCount, sessions)
	}
	mean := float64(sessions*SASWordTotal) / float64(SASWordCount)
	for w, n := range wordUse {
		if float64(n) > mean*4 {
			t.Errorf("word %q appeared %d times, far above the uniform expectation of %.1f", w, n, mean)
		}
	}
}
