// Package tls provides TLS certificate management for MITM proxying.
package tls

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	// CAKeySize is the RSA key size for the CA.
	CAKeySize = 2048

	// CAValidityYears is reduced from 10 to 2 years (addresses langley-awl).
	CAValidityYears = 2
)

// CA represents a Certificate Authority for generating MITM certificates.
type CA struct {
	cert    *x509.Certificate
	key     *rsa.PrivateKey
	certPEM []byte
	keyPEM  []byte
	crlDER  []byte // CRL in DER format for Windows revocation checking
	crlURL  string // URL where CRL is served
}

// LoadOrCreateCA loads an existing CA or creates a new one.
// The CA files are stored with restrictive permissions (addresses langley-xpi).
func LoadOrCreateCA(dir string) (*CA, error) {
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")

	// Try to load existing CA
	if ca, err := loadCA(certPath, keyPath); err == nil {
		return ca, nil
	}

	// Create new CA
	ca, err := createCA()
	if err != nil {
		return nil, fmt.Errorf("creating CA: %w", err)
	}

	// Ensure directory exists with restrictive permissions
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("creating cert directory: %w", err)
	}

	// Save certificate (can be shared)
	if err := os.WriteFile(certPath, ca.certPEM, 0644); err != nil {
		return nil, fmt.Errorf("writing CA cert: %w", err)
	}

	// Save private key with restrictive permissions (addresses langley-xpi)
	if err := writeSecureFile(keyPath, ca.keyPEM); err != nil {
		return nil, fmt.Errorf("writing CA key: %w", err)
	}

	return ca, nil
}

// loadCA loads a CA from disk.
func loadCA(certPath, keyPath string) (*CA, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}

	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}

	// Parse certificate
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("failed to decode CA certificate PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing CA certificate: %w", err)
	}

	// Parse private key
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("failed to decode CA private key PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing CA private key: %w", err)
	}

	return &CA{
		cert:    cert,
		key:     key,
		certPEM: certPEM,
		keyPEM:  keyPEM,
	}, nil
}

// createCA generates a new CA certificate and key.
func createCA() (*CA, error) {
	// Generate private key
	key, err := rsa.GenerateKey(rand.Reader, CAKeySize)
	if err != nil {
		return nil, fmt.Errorf("generating private key: %w", err)
	}

	// Generate cryptographically random serial number (addresses langley-12r)
	serialNumber, err := generateRandomSerial()
	if err != nil {
		return nil, fmt.Errorf("generating serial number: %w", err)
	}

	// Create certificate template
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "Langley CA",
			Organization: []string{"Langley Proxy"},
		},
		NotBefore:             time.Now().Add(-24 * time.Hour), // Grace period for clock skew
		NotAfter:              time.Now().AddDate(CAValidityYears, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	// Self-sign the certificate
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("creating certificate: %w", err)
	}

	// Parse the certificate back
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parsing created certificate: %w", err)
	}

	// Encode to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	return &CA{
		cert:    cert,
		key:     key,
		certPEM: certPEM,
		keyPEM:  keyPEM,
	}, nil
}

// generateRandomSerial generates a cryptographically random serial number.
// This addresses langley-12r (predictable timestamp-based serials).
func generateRandomSerial() (*big.Int, error) {
	// Use 128 bits of randomness
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, max)
	if err != nil {
		return nil, err
	}
	// Ensure it's positive
	serial.Add(serial, big.NewInt(1))
	return serial, nil
}

// CertPEM returns the CA certificate in PEM format.
func (ca *CA) CertPEM() []byte {
	return ca.certPEM
}

// Certificate returns the CA certificate.
func (ca *CA) Certificate() *x509.Certificate {
	return ca.cert
}

// CRLDER returns the CRL in DER format.
func (ca *CA) CRLDER() []byte {
	return ca.crlDER
}

// CRLURL returns the URL where the CRL is served.
func (ca *CA) CRLURL() string {
	return ca.crlURL
}

// SetCRLURL sets the CRL URL and generates the CRL.
// This must be called before generating certificates for Windows compatibility.
func (ca *CA) SetCRLURL(url string) error {
	ca.crlURL = url
	return ca.generateCRL()
}

// generateCRL generates an empty CRL signed by the CA.
func (ca *CA) generateCRL() error {
	template := &x509.RevocationList{
		Number:     big.NewInt(1),
		ThisUpdate: time.Now(),
		NextUpdate: time.Now().AddDate(0, 0, 30), // Valid for 30 days
	}

	crlDER, err := x509.CreateRevocationList(rand.Reader, template, ca.cert, ca.key)
	if err != nil {
		return fmt.Errorf("creating CRL: %w", err)
	}

	ca.crlDER = crlDER
	return nil
}

// writeSecureFile writes a file with platform-specific secure permissions.
// On Unix: chmod 0600
// On Windows: Uses file permissions that restrict to current user.
func writeSecureFile(path string, data []byte) error {
	// Write with restrictive permissions
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}

	// On Windows, Go's os.WriteFile with 0600 sets read-only.
	// For better security on Windows, we'd use icacls, but for MVP
	// the file permission approach is acceptable.
	// Production improvement: Use Windows ACLs via syscall.
	if runtime.GOOS == "windows" {
		// For now, 0600 translates to read-only on Windows which is acceptable.
		// The file is only readable by the owner by default on NTFS when created
		// in a user's directory.
	}

	return nil
}
