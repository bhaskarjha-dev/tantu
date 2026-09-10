package pairing

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

func TestGenerateIdentity(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity failed: %v", err)
	}

	if len(id.CertPEM) == 0 {
		t.Fatal("CertPEM is empty")
	}
	if len(id.KeyPEM) == 0 {
		t.Fatal("KeyPEM is empty")
	}
	if len(id.Fingerprint) != 64 { // SHA-256 hex string is 64 chars
		t.Fatalf("Fingerprint length %d != 64", len(id.Fingerprint))
	}

	// Verify certificate
	certBlock, _ := pem.Decode(id.CertPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		t.Fatalf("invalid cert PEM block: %v", certBlock)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatalf("failed to parse certificate: %v", err)
	}

	if cert.Subject.CommonName != "tantu" {
		t.Errorf("expected CN=tantu, got %s", cert.Subject.CommonName)
	}

	// Key type
	pubKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("expected *ecdsa.PublicKey, got %T", cert.PublicKey)
	}
	if pubKey.Curve != elliptic.P256() {
		t.Errorf("expected P-256 curve, got %v", pubKey.Curve.Params().Name)
	}

	// Validity duration
	validity := cert.NotAfter.Sub(cert.NotBefore)
	if validity < 9*365*24*time.Hour || validity > 11*365*24*time.Hour {
		t.Errorf("unexpected validity duration: %v", validity)
	}

	// Key usage
	expectedUsage := x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
	if cert.KeyUsage&expectedUsage != expectedUsage {
		t.Errorf("missing key usage flags: %v", cert.KeyUsage)
	}

	hasClientAuth := false
	hasServerAuth := false
	for _, u := range cert.ExtKeyUsage {
		if u == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
		}
		if u == x509.ExtKeyUsageServerAuth {
			hasServerAuth = true
		}
	}
	if !hasClientAuth || !hasServerAuth {
		t.Errorf("missing ExtKeyUsage: clientAuth=%v, serverAuth=%v", hasClientAuth, hasServerAuth)
	}

	// Verify private key
	keyBlock, _ := pem.Decode(id.KeyPEM)
	if keyBlock == nil || keyBlock.Type != "EC PRIVATE KEY" {
		t.Fatalf("invalid key PEM block: %v", keyBlock)
	}
	privKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatalf("failed to parse ec private key: %v", err)
	}
	if privKey.Curve != elliptic.P256() {
		t.Errorf("expected private key curve P-256, got %v", privKey.Curve.Params().Name)
	}

	// Verify Fingerprint calculation matches id.Fingerprint
	fp, err := Fingerprint(id.CertPEM)
	if err != nil {
		t.Fatalf("Fingerprint failed: %v", err)
	}
	if fp != id.Fingerprint {
		t.Errorf("Fingerprint mismatch: got %s, id has %s", fp, id.Fingerprint)
	}
}

func TestFingerprint(t *testing.T) {
	// Invalid PEM
	_, err := Fingerprint([]byte("not a pem"))
	if err == nil {
		t.Error("expected error for invalid PEM data")
	}

	// Wrong block type
	wrongBlock := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: []byte("dummy"),
	})
	_, err = Fingerprint(wrongBlock)
	if err == nil {
		t.Error("expected error for non-CERTIFICATE block type")
	}

	// Determinism
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	fp1, _ := Fingerprint(id.CertPEM)
	fp2, _ := Fingerprint(id.CertPEM)
	if fp1 != fp2 {
		t.Errorf("fingerprint not deterministic: %s != %s", fp1, fp2)
	}
}

func TestSASCode(t *testing.T) {
	fp := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	sas := SASCode(fp)
	if sas != "e3b0c4" {
		t.Errorf("expected e3b0c4, got %s", sas)
	}
	if len(sas) != 6 {
		t.Errorf("expected SAS length 6, got %d", len(sas))
	}

	// Short input fallback
	short := "abc"
	if SASCode(short) != "abc" {
		t.Errorf("expected abc, got %s", SASCode(short))
	}
}
