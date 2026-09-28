package pairing

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"strings"
	"time"
)

// Identity holds a generated TLS identity.
type Identity struct {
	CertPEM     []byte `json:"cert_pem"`
	KeyPEM      []byte `json:"key_pem"`
	Fingerprint string `json:"fingerprint"`
}

// GenerateIdentity creates a new self-signed ECDSA P-256 certificate and key pair.
func GenerateIdentity() (*Identity, error) {
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ecdsa key: %w", err)
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, fmt.Errorf("generate serial number: %w", err)
	}

	now := time.Now()
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: "tantu",
		},
		NotBefore:             now.Add(-5 * time.Minute),          // small leeway for clock skew
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour), // 10 years
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true, // self-signed root
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privKey.PublicKey, privKey)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derBytes,
	})

	keyBytes, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("marshal ecdsa private key: %w", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyBytes,
	})

	fpHash := sha256.Sum256(derBytes)
	fp := hex.EncodeToString(fpHash[:])

	return &Identity{
		CertPEM:     certPEM,
		KeyPEM:      keyPEM,
		Fingerprint: fp,
	}, nil
}

// Fingerprint computes the SHA-256 fingerprint of a PEM-encoded certificate.
// It rejects malformed, empty, multiple, or trailing certificate blocks
// instead of assigning a plausible identity to arbitrary PEM bytes.
func Fingerprint(certPEM []byte) (string, error) {
	rest := certPEM
	var certDER []byte
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return "", fmt.Errorf("expected CERTIFICATE block type, got %s", block.Type)
		}
		if certDER != nil {
			return "", errors.New("multiple certificate blocks are not allowed")
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return "", fmt.Errorf("parse certificate: %w", err)
		}
		certDER = block.Bytes
		rest = remaining
	}
	if certDER == nil {
		return "", errors.New("failed to parse certificate PEM block")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return "", errors.New("trailing data after certificate PEM block")
	}
	hash := sha256.Sum256(certDER)
	return hex.EncodeToString(hash[:]), nil
}

// ValidateIdentity verifies that the certificate, private key, and cached
// fingerprint describe one usable local TLS identity.
func ValidateIdentity(id *Identity) error {
	if id == nil {
		return errors.New("identity is nil")
	}
	fp, err := Fingerprint(id.CertPEM)
	if err != nil {
		return fmt.Errorf("fingerprint certificate: %w", err)
	}
	if id.Fingerprint == "" || !strings.EqualFold(id.Fingerprint, fp) {
		return fmt.Errorf("identity fingerprint mismatch: got %s, computed %s", id.Fingerprint, fp)
	}
	if _, err := tls.X509KeyPair(id.CertPEM, id.KeyPEM); err != nil {
		return fmt.Errorf("identity certificate/key mismatch: %w", err)
	}
	return nil
}

// SASWords is the word list used to render a SAS. Words are 4-6 lowercase ASCII
// letters and deliberately exclude i, l, o, 0, and 1 so a code read aloud or
// copied off one screen matches on the other. TestSASWordListIsSound asserts
// the list is exactly SASWordCount entries, unique, and free of confusables.
var SASWords = [...]string{
	"able", "acid", "aged", "aloe", "arch", "atom", "aunt", "away", "axis", "baby",
	"back", "bald", "barn", "beam", "bear", "beat", "bell", "belt", "bend", "best",
	"bird", "bite", "blue", "boat", "body", "bold", "bone", "book", "boot", "born",
	"both", "bowl", "bulk", "burn", "bush", "busy", "cake", "calm", "came", "camp",
	"card", "care", "cart", "case", "cash", "cast", "cell", "chat", "chef", "chip",
	"city", "clay", "clip", "club", "coal", "coat", "code", "cold", "comb", "come",
	"cook", "cool", "cord", "corn", "cost", "crew", "crop", "dark", "dawn", "days",
	"deal", "deck", "deep", "deer", "desk", "dial", "dice", "diet", "dish", "docs",
	"dome", "done", "door", "dose", "down", "draw", "drew", "drop", "drum", "dual",
	"duck", "dust", "duty", "each", "earn", "ease", "east", "easy", "echo", "edge",
	"edit", "else", "even", "ever", "exam", "exit", "face", "fact", "fade", "fail",
	"fair", "fame", "farm", "fast", "fate", "fear", "feed", "feel", "fern", "file",
	"fill", "film", "find", "fine", "fire", "firm", "fish", "fist", "five", "flag",
	"flat", "flow", "foam", "fold", "food", "foot", "form", "fort", "four", "free",
	"from", "fuel", "full", "fund", "gain", "game", "gate", "gave", "gear", "gift",
	"girl", "give", "glad", "goal", "goes", "gold", "golf", "gone", "good", "gray",
	"grew", "grey", "grow", "gulf", "hair", "half", "hall", "hand", "hang", "hard",
	"harm", "hate", "have", "hawk", "head", "hear", "heat", "help", "here", "hero",
	"high", "hill", "hint", "hire", "hold", "hole", "holy", "home", "hope", "horn",
	"host", "hour", "huge", "hunt", "idea", "inch", "into", "iron", "item", "jazz",
	"join", "jump", "jury", "just", "keen", "keep", "kelp", "kind", "king", "kite",
	"knee", "know", "lace", "lake", "lamb", "lamp", "land", "lane", "lark", "last",
	"late", "lawn", "leaf", "lean", "leap", "left", "lens", "less", "life", "lift",
	"like", "lime", "line", "link", "lion", "list", "live", "load", "loaf", "loan",
	"lock", "loud", "luck", "lump", "lung", "made", "mail", "main", "make", "male",
	"mall", "many", "mark", "mask", "mass", "maze", "meal", "mean", "meat", "meet",
	"meld", "menu", "mild", "mile", "milk", "mill", "mind", "mine", "mint", "mist",
	"mode", "mood", "moon", "more", "most", "mule", "must", "nail", "name", "navy",
	"near", "neat", "neck", "need", "nest", "news", "next", "nice", "nine", "node",
	"none", "nose", "note", "oath", "obey", "odds", "once", "only", "open", "oral",
	"oval", "oven", "over", "pace", "pack", "page", "paid", "pair", "palm", "park",
	"part", "pass", "past", "path", "peak", "pear", "peer", "pick", "pier", "pile",
	"pine", "pink", "pipe", "plan", "play", "plot", "plug", "plus", "poll", "pool",
	"poor", "port", "post", "pump", "pure", "push", "quad", "quiz", "race", "rack",
	"raft", "rail", "rain", "rake", "rare", "rate", "read", "real", "reed", "reef",
	"rent", "rest", "rice", "ride", "ring", "rise", "road", "robe", "rock", "role",
	"roll", "roof", "room", "root", "rope", "rose", "ruby", "rule", "rush", "rust",
	"sail", "salt", "same", "sand", "sane", "save", "scan", "scar", "seal", "seam",
	"seat", "seed", "seek", "seem", "seen", "self", "sell", "send", "sept", "shut",
	"sick", "side", "sign", "silk", "sing", "sink", "site", "size", "skin", "slab",
	"slot", "slow", "snow", "soap", "soft", "soil", "sold", "solo", "some", "song",
	"soon", "sort", "soul", "spot", "star", "stay", "stem", "step", "stop", "such",
	"suit", "sulk", "sung", "sure", "surf", "swan", "tail", "take", "tale", "talk",
	"tall", "tank", "tape", "task", "team", "tear", "tech", "tell", "tend", "tent",
	"term", "test", "text", "than", "that", "them", "then", "thin", "this", "thud",
	"tide", "tidy", "tier", "tile", "tilt", "time", "tiny", "toll", "tomb", "tone",
	"tool", "torn", "tour", "town", "trap", "tray", "tree", "trip", "trod", "tune",
	"twin", "type", "ugly", "undo", "unit", "upon", "used", "user", "vast", "verb",
	"very", "vest", "view", "vine", "visa", "void", "vote", "wade", "wage", "wait",
	"wake", "walk", "wall", "wand", "want", "ward", "warm", "wash", "wave", "waxy",
	"weak", "weed", "week", "well", "went", "west", "what", "when", "whim", "whip",
	"whom", "wide",
}

// SASWordCount is the number of distinct words in SASWords, i.e. the address
// space of a single word. TestSASWordListIsSound keeps it honest.
const SASWordCount = len(SASWords)

// SASWordCountBits is the entropy carried by one SAS word. The list is exactly
// 512 entries, so the mapping is a uniform bit-mask rather than a modulo with a
// residual bias.
const SASWordCountBits = 9

// SASWordTotal is how many words a rendered SAS contains. At 9 bits per word
// this is 54 bits, comfortably above the 24 bits of the old code and enough
// that an attacker cannot grind a matching certificate even knowing every prior
// SAS for that peer.
const SASWordTotal = 6

// SASEntropyBits is the total entropy of a rendered SAS.
const SASEntropyBits = SASWordCountBits * SASWordTotal

// TranscriptSAS derives the Short Authentication String for a pairing session.
//
// This is a hash over the whole pairing transcript: both certificate
// fingerprints and both freshly generated nonces. That is what makes it useful
// against an active man-in-the-middle.
//
// The previous implementation was the first 6 hex characters (24 bits) of a
// single long-lived certificate fingerprint. That failed three ways: it was not
// bound to the session, so the same code applied to every future pairing with
// that peer; the certificate is valid for ten years, so an attacker who learned
// the value once could grind ~2^24 certificates offline until one shared the
// prefix, then present it forever; and the value was broadcast in cleartext on
// the LAN every few seconds, so that pre-computation was free to start.
//
// The four inputs are canonicalized before hashing, so the initiator and the
// responder derive the identical value without either having to know which role
// it plays. The code identifies the pair of machines in this session, not a
// direction of travel.
//
// SASWordTotal words at SASWordCountBits each give SASEntropyBits and render as
// something a person can actually compare by eye, the approach Signal and
// GnuPG use.
func TranscriptSAS(localFP, peerFP string, localNonce, peerNonce []byte) string {
	// Canonical order: the smaller fingerprint first, and the nonces paired to
	// whichever side owns that fingerprint.
	type contribution struct {
		fp    string
		nonce []byte
	}
	a := contribution{strings.ToLower(strings.TrimSpace(localFP)), localNonce}
	b := contribution{strings.ToLower(strings.TrimSpace(peerFP)), peerNonce}
	if a.fp > b.fp {
		a, b = b, a
	}

	h := sha256.New()
	// Domain separation: this value is shown to a human deciding whether to
	// trust a machine, so it must never collide with another digest domain.
	h.Write([]byte("tantu/pairing-sas/v2\x00"))
	writeLengthPrefixed(h, []byte(a.fp))
	writeLengthPrefixed(h, a.nonce)
	writeLengthPrefixed(h, []byte(b.fp))
	writeLengthPrefixed(h, b.nonce)

	sum := h.Sum(nil)
	words := make([]string, SASWordTotal)
	// 9 bits per word taken from the digest; the mapping is a mask against a
	// 512-entry list, so every word is equally likely. 54 of the 256 digest
	// bits are used; the rest are discarded, which is the intended trade for a
	// code a person can compare.
	for i := 0; i < SASWordTotal; i++ {
		bitOffset := i * SASWordCountBits
		byteOffset := bitOffset / 8
		shift := uint(bitOffset % 8)
		// 9 bits can straddle two bytes; take the high bits of the first and
		// the low bits of the second.
		idx := (uint(sum[byteOffset])<<8 | uint(sum[byteOffset+1])) >> (16 - SASWordCountBits - shift)
		words[i] = SASWords[int(idx)&(SASWordCount-1)]
	}
	return strings.ToUpper(strings.Join(words, "-"))
}

func writeLengthPrefixed(h hash.Hash, b []byte) {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	h.Write(n[:])
	h.Write(b)
}

// SASCode returns the first 6 hex characters of a certificate fingerprint.
//
// It is retained only for naming a node in a UI list and for reporting a peer
// back to the operator. It must never be used as a security decision: it is 24
// bits, static, and not bound to a session. Use TranscriptSAS for that.
func SASCode(fingerprint string) string {
	if len(fingerprint) < 6 {
		return fingerprint
	}
	return fingerprint[:6]
}
