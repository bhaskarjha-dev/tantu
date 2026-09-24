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
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
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

// SASCode returns the first 6 hex characters of a certificate fingerprint
// for human-readable visual verification during pairing.
func SASCode(fingerprint string) string {
	if len(fingerprint) < 6 {
		return fingerprint
	}
	return fingerprint[:6]
}
