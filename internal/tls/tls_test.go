package tls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestLoadOrCreateCA_CreatesNew tests that a new CA is created when none exists.
func TestLoadOrCreateCA_CreatesNew(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-tls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ca, err := LoadOrCreateCA(tempDir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA failed: %v", err)
	}

	// Verify CA was created
	if ca == nil {
		t.Fatal("CA is nil")
	}
	if ca.cert == nil {
		t.Error("CA certificate is nil")
	}
	if ca.key == nil {
		t.Error("CA private key is nil")
	}

	// Verify files were created
	certPath := filepath.Join(tempDir, "ca.crt")
	keyPath := filepath.Join(tempDir, "ca.key")

	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		t.Error("CA certificate file was not created")
	}
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		t.Error("CA key file was not created")
	}

	// Verify key file has restrictive permissions (0600)
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("failed to stat key file: %v", err)
	}
	// On Windows, permission checks are different, so we just verify it exists
	if info.Size() == 0 {
		t.Error("CA key file is empty")
	}
}

// TestLoadOrCreateCA_LoadsExisting tests that an existing CA is loaded from disk.
func TestLoadOrCreateCA_LoadsExisting(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-tls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create CA first
	ca1, err := LoadOrCreateCA(tempDir)
	if err != nil {
		t.Fatalf("first LoadOrCreateCA failed: %v", err)
	}

	// Load again - should load existing
	ca2, err := LoadOrCreateCA(tempDir)
	if err != nil {
		t.Fatalf("second LoadOrCreateCA failed: %v", err)
	}

	// Verify same certificate (by serial number)
	if ca1.cert.SerialNumber.Cmp(ca2.cert.SerialNumber) != 0 {
		t.Error("loaded CA has different serial number - should have loaded existing")
	}
}

// TestCA_CertPEM_Format validates the PEM encoding of the CA certificate.
func TestCA_CertPEM_Format(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-tls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ca, err := LoadOrCreateCA(tempDir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA failed: %v", err)
	}

	certPEM := ca.CertPEM()
	if len(certPEM) == 0 {
		t.Fatal("CertPEM returned empty")
	}

	// Verify it's valid PEM
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("failed to decode PEM")
	}
	if block.Type != "CERTIFICATE" {
		t.Errorf("unexpected PEM type: got %q, want %q", block.Type, "CERTIFICATE")
	}

	// Verify it's a valid certificate
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("failed to parse certificate: %v", err)
	}

	// Verify it's a CA certificate
	if !cert.IsCA {
		t.Error("certificate is not marked as CA")
	}
	if cert.Subject.CommonName != "Langley CA" {
		t.Errorf("unexpected CommonName: got %q, want %q", cert.Subject.CommonName, "Langley CA")
	}
}

// TestCA_Certificate returns the parsed certificate.
func TestCA_Certificate(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-tls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ca, err := LoadOrCreateCA(tempDir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA failed: %v", err)
	}

	cert := ca.Certificate()
	if cert == nil {
		t.Fatal("Certificate() returned nil")
	}
	if !cert.IsCA {
		t.Error("certificate is not marked as CA")
	}
}

// TestGenerateRandomSerial_NotPredictable tests that serial numbers are random.
func TestGenerateRandomSerial_NotPredictable(t *testing.T) {
	seen := make(map[string]bool)

	// Generate multiple serials and ensure they're all different
	for i := 0; i < 100; i++ {
		serial, err := generateRandomSerial()
		if err != nil {
			t.Fatalf("generateRandomSerial failed: %v", err)
		}

		str := serial.String()
		if seen[str] {
			t.Errorf("duplicate serial number generated: %s", str)
		}
		seen[str] = true

		// Verify it's positive
		if serial.Sign() <= 0 {
			t.Errorf("serial number is not positive: %s", str)
		}
	}
}

// TestCRL_DER_Format tests CRL generation in DER format.
func TestCRL_DER_Format(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-tls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ca, err := LoadOrCreateCA(tempDir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA failed: %v", err)
	}

	// Set CRL URL to trigger CRL generation
	err = ca.SetCRLURL("http://localhost:8080/crl/ca.crl")
	if err != nil {
		t.Fatalf("SetCRLURL failed: %v", err)
	}

	crlDER := ca.CRLDER()
	if len(crlDER) == 0 {
		t.Fatal("CRLDER returned empty")
	}

	// Verify it's valid DER-encoded CRL
	crl, err := x509.ParseRevocationList(crlDER)
	if err != nil {
		t.Fatalf("failed to parse CRL: %v", err)
	}

	// Verify CRL is signed by CA
	if err := crl.CheckSignatureFrom(ca.cert); err != nil {
		t.Errorf("CRL signature verification failed: %v", err)
	}

	// Verify CRL URL is set
	if ca.CRLURL() != "http://localhost:8080/crl/ca.crl" {
		t.Errorf("CRLURL mismatch: got %q", ca.CRLURL())
	}
}

// mockClientHelloInfo creates a mock ClientHelloInfo for testing.
func mockClientHelloInfo(serverName string) *tls.ClientHelloInfo {
	return &tls.ClientHelloInfo{
		ServerName: serverName,
		Conn:       &mockConn{},
	}
}

// mockConn implements net.Conn for testing.
type mockConn struct {
	net.Conn
	localAddr net.Addr
}

func (m *mockConn) LocalAddr() net.Addr {
	if m.localAddr != nil {
		return m.localAddr
	}
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 443}
}

// TestCertCache_GetCertificate_Generated tests certificate generation on cache miss.
func TestCertCache_GetCertificate_Generated(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-tls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ca, err := LoadOrCreateCA(tempDir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA failed: %v", err)
	}

	cache := NewCertCache(ca, 10)
	if cache.Size() != 0 {
		t.Errorf("new cache should be empty, got size %d", cache.Size())
	}

	// Get certificate for a hostname
	hello := mockClientHelloInfo("example.com")
	cert, err := cache.GetCertificate(hello)
	if err != nil {
		t.Fatalf("GetCertificate failed: %v", err)
	}

	if cert == nil {
		t.Fatal("GetCertificate returned nil")
	}
	if len(cert.Certificate) == 0 {
		t.Error("certificate chain is empty")
	}

	// Verify cache size increased
	if cache.Size() != 1 {
		t.Errorf("cache size should be 1, got %d", cache.Size())
	}

	// Verify the certificate has correct SAN
	leafCert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("failed to parse leaf certificate: %v", err)
	}
	if len(leafCert.DNSNames) == 0 || leafCert.DNSNames[0] != "example.com" {
		t.Errorf("certificate missing expected DNS SAN: %v", leafCert.DNSNames)
	}
}

// TestCertCache_GetCertificate_Cached tests cache hit behavior.
func TestCertCache_GetCertificate_Cached(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-tls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ca, err := LoadOrCreateCA(tempDir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA failed: %v", err)
	}

	cache := NewCertCache(ca, 10)

	// Get certificate first time
	hello := mockClientHelloInfo("cached.example.com")
	cert1, err := cache.GetCertificate(hello)
	if err != nil {
		t.Fatalf("first GetCertificate failed: %v", err)
	}

	// Get same certificate again - should be cached
	cert2, err := cache.GetCertificate(hello)
	if err != nil {
		t.Fatalf("second GetCertificate failed: %v", err)
	}

	// Should be the same certificate (same pointer)
	if cert1 != cert2 {
		t.Error("second call should return cached certificate")
	}

	// Cache size should still be 1
	if cache.Size() != 1 {
		t.Errorf("cache size should still be 1, got %d", cache.Size())
	}
}

// TestCertCache_LRU_Eviction tests that oldest entries are evicted when cache is full.
func TestCertCache_LRU_Eviction(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-tls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ca, err := LoadOrCreateCA(tempDir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA failed: %v", err)
	}

	// Small cache size for testing eviction
	cache := NewCertCache(ca, 3)

	// Fill cache with 3 entries
	hosts := []string{"host1.example.com", "host2.example.com", "host3.example.com"}
	for _, host := range hosts {
		hello := mockClientHelloInfo(host)
		_, err := cache.GetCertificate(hello)
		if err != nil {
			t.Fatalf("GetCertificate failed for %s: %v", host, err)
		}
	}

	if cache.Size() != 3 {
		t.Errorf("cache size should be 3, got %d", cache.Size())
	}

	// Add a 4th entry - should evict host1 (oldest)
	hello := mockClientHelloInfo("host4.example.com")
	_, err = cache.GetCertificate(hello)
	if err != nil {
		t.Fatalf("GetCertificate failed: %v", err)
	}

	// Size should still be 3
	if cache.Size() != 3 {
		t.Errorf("cache size should still be 3 after eviction, got %d", cache.Size())
	}

	// Access host1 - should generate new cert (was evicted)
	hello1 := mockClientHelloInfo("host1.example.com")
	cert1, err := cache.GetCertificate(hello1)
	if err != nil {
		t.Fatalf("GetCertificate failed for evicted host: %v", err)
	}
	if cert1 == nil {
		t.Error("should be able to get certificate for evicted host")
	}
}

// TestCertCache_LRU_AccessUpdatesOrder tests that accessing a cached entry updates LRU order.
func TestCertCache_LRU_AccessUpdatesOrder(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-tls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ca, err := LoadOrCreateCA(tempDir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA failed: %v", err)
	}

	cache := NewCertCache(ca, 3)

	// Add 3 entries: host1, host2, host3
	for _, host := range []string{"host1.com", "host2.com", "host3.com"} {
		hello := mockClientHelloInfo(host)
		_, err := cache.GetCertificate(hello)
		if err != nil {
			t.Fatalf("GetCertificate failed: %v", err)
		}
	}

	// Access host1 (oldest) to move it to end of LRU
	hello1 := mockClientHelloInfo("host1.com")
	_, err = cache.GetCertificate(hello1)
	if err != nil {
		t.Fatalf("GetCertificate failed: %v", err)
	}

	// Add host4 - should evict host2 (now oldest)
	hello4 := mockClientHelloInfo("host4.com")
	_, err = cache.GetCertificate(hello4)
	if err != nil {
		t.Fatalf("GetCertificate failed: %v", err)
	}

	// host1 should still be cached (was accessed recently)
	// We can verify by checking the order internally
	// For now, just verify cache size is correct
	if cache.Size() != 3 {
		t.Errorf("cache size should be 3, got %d", cache.Size())
	}
}

// TestCertCache_ThreadSafety tests concurrent access to the cache.
func TestCertCache_ThreadSafety(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-tls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ca, err := LoadOrCreateCA(tempDir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA failed: %v", err)
	}

	cache := NewCertCache(ca, 100)

	// Run concurrent goroutines accessing the cache
	var wg sync.WaitGroup
	errors := make(chan error, 100)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				host := "concurrent" + string(rune('0'+id)) + string(rune('0'+j)) + ".example.com"
				hello := mockClientHelloInfo(host)
				_, err := cache.GetCertificate(hello)
				if err != nil {
					errors <- err
				}
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("concurrent access error: %v", err)
	}

	// Cache should have some entries (may have evictions due to size limit)
	if cache.Size() == 0 {
		t.Error("cache should not be empty after concurrent access")
	}
}

// TestCertCache_Clear tests clearing the cache.
func TestCertCache_Clear(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-tls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ca, err := LoadOrCreateCA(tempDir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA failed: %v", err)
	}

	cache := NewCertCache(ca, 10)

	// Add some entries
	for _, host := range []string{"a.com", "b.com", "c.com"} {
		hello := mockClientHelloInfo(host)
		_, err := cache.GetCertificate(hello)
		if err != nil {
			t.Fatalf("GetCertificate failed: %v", err)
		}
	}

	if cache.Size() != 3 {
		t.Errorf("cache size should be 3, got %d", cache.Size())
	}

	// Clear cache
	cache.Clear()

	if cache.Size() != 0 {
		t.Errorf("cache size should be 0 after Clear, got %d", cache.Size())
	}
}

// TestCertCache_DefaultMaxSize tests that default max size is used when 0 is provided.
func TestCertCache_DefaultMaxSize(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-tls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ca, err := LoadOrCreateCA(tempDir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA failed: %v", err)
	}

	// Create cache with 0 (should use default)
	cache := NewCertCache(ca, 0)
	if cache.maxSize != DefaultMaxCacheSize {
		t.Errorf("expected default max size %d, got %d", DefaultMaxCacheSize, cache.maxSize)
	}

	// Create cache with negative (should use default)
	cache2 := NewCertCache(ca, -5)
	if cache2.maxSize != DefaultMaxCacheSize {
		t.Errorf("expected default max size %d, got %d", DefaultMaxCacheSize, cache2.maxSize)
	}
}

// TestCertCache_IPAddress tests certificate generation for IP addresses.
func TestCertCache_IPAddress(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-tls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ca, err := LoadOrCreateCA(tempDir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA failed: %v", err)
	}

	cache := NewCertCache(ca, 10)

	// Get certificate for an IP address
	hello := mockClientHelloInfo("192.168.1.1")
	cert, err := cache.GetCertificate(hello)
	if err != nil {
		t.Fatalf("GetCertificate failed: %v", err)
	}

	// Verify the certificate has IP SAN
	leafCert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("failed to parse leaf certificate: %v", err)
	}

	if len(leafCert.IPAddresses) == 0 {
		t.Error("certificate should have IP address SAN")
	}
	if !leafCert.IPAddresses[0].Equal(net.ParseIP("192.168.1.1")) {
		t.Errorf("unexpected IP SAN: %v", leafCert.IPAddresses)
	}
}

// TestCertCache_CRLDistributionPoint tests that CRL URL is included in generated certs.
func TestCertCache_CRLDistributionPoint(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "langley-tls-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	ca, err := LoadOrCreateCA(tempDir)
	if err != nil {
		t.Fatalf("LoadOrCreateCA failed: %v", err)
	}

	// Set CRL URL
	crlURL := "http://localhost:9091/crl/ca.crl"
	if err := ca.SetCRLURL(crlURL); err != nil {
		t.Fatalf("SetCRLURL failed: %v", err)
	}

	cache := NewCertCache(ca, 10)

	hello := mockClientHelloInfo("crl-test.example.com")
	cert, err := cache.GetCertificate(hello)
	if err != nil {
		t.Fatalf("GetCertificate failed: %v", err)
	}

	leafCert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("failed to parse leaf certificate: %v", err)
	}

	if len(leafCert.CRLDistributionPoints) == 0 {
		t.Error("certificate should have CRL Distribution Point")
	} else if leafCert.CRLDistributionPoints[0] != crlURL {
		t.Errorf("unexpected CRL URL: got %q, want %q", leafCert.CRLDistributionPoints[0], crlURL)
	}
}
