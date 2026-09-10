package pairing

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
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
		NotBefore:             now.Add(-5 * time.Minute), // small leeway for clock skew
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
// Returns a lowercase hex string without colons.
func Fingerprint(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", errors.New("failed to parse certificate PEM block")
	}
	if block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("expected CERTIFICATE block type, got %s", block.Type)
	}

	hash := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(hash[:]), nil
}

// SASCode returns the first 6 hex characters of a certificate fingerprint
// for human-readable visual verification during pairing.
func SASCode(fingerprint string) string {
	if len(fingerprint) < 6 {
		return fingerprint
	}
	return fingerprint[:6]
}
