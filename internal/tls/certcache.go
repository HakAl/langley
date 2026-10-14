package tls

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"net"
	"sync"
	"time"
)

const (
	// CertKeySize is the RSA key size for generated certificates.
	CertKeySize = 2048

	// CertValidityDays is the validity period for generated certificates.
	CertValidityDays = 30

	// DefaultMaxCacheSize is the default LRU cache size (addresses langley-bma).
	DefaultMaxCacheSize = 1000
)

// CertCache is an LRU cache for dynamically generated TLS certificates.
// This addresses langley-bma (unbounded cache leading to memory exhaustion).
type CertCache struct {
	ca       *CA
	maxSize  int
	mu       sync.Mutex
	cache    map[string]*cacheEntry
	order    []string // LRU order (oldest first)
}

type cacheEntry struct {
	cert      *tls.Certificate
	createdAt time.Time
}

// NewCertCache creates a new certificate cache with the given CA and max size.
func NewCertCache(ca *CA, maxSize int) *CertCache {
	if maxSize <= 0 {
		maxSize = DefaultMaxCacheSize
	}
	return &CertCache{
		ca:      ca,
		maxSize: maxSize,
		cache:   make(map[string]*cacheEntry),
		order:   make([]string, 0, maxSize),
	}
}

// GetCertificate returns a TLS certificate for the given hostname.
// If not cached, generates a new certificate signed by the CA.
func (c *CertCache) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := hello.ServerName
	if host == "" {
		// Fallback to connection address if no SNI
		if addr, ok := hello.Conn.LocalAddr().(*net.TCPAddr); ok {
			host = addr.IP.String()
		} else {
			return nil, fmt.Errorf("no server name in ClientHello")
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Check cache
	if entry, ok := c.cache[host]; ok {
		// Move to end of LRU order (most recently used)
		c.moveToEnd(host)
		return entry.cert, nil
	}

	// Generate new certificate
	cert, err := c.generateCert(host)
	if err != nil {
		return nil, fmt.Errorf("generating certificate for %s: %w", host, err)
	}

	// Evict if at capacity
	if len(c.cache) >= c.maxSize {
		c.evictOldest()
	}

	// Add to cache
	c.cache[host] = &cacheEntry{
		cert:      cert,
		createdAt: time.Now(),
	}
	c.order = append(c.order, host)

	return cert, nil
}

// generateCert generates a TLS certificate for the given hostname.
func (c *CertCache) generateCert(host string) (*tls.Certificate, error) {
	// Generate private key
	key, err := rsa.GenerateKey(rand.Reader, CertKeySize)
	if err != nil {
		return nil, fmt.Errorf("generating key: %w", err)
	}

	// Generate random serial (addresses langley-12r)
	serial, err := generateRandomSerial()
	if err != nil {
		return nil, fmt.Errorf("generating serial: %w", err)
	}

	// Create certificate template
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   host,
			Organization: []string{"Langley Proxy"},
		},
		NotBefore:             time.Now().Add(-24 * time.Hour),
		NotAfter:              time.Now().AddDate(0, 0, CertValidityDays),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// Add CRL Distribution Point for Windows compatibility (langley-2qj)
	if crlURL := c.ca.CRLURL(); crlURL != "" {
		template.CRLDistributionPoints = []string{crlURL}
	}

	// Add host as SAN
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}

	// Sign with CA
	certDER, err := x509.CreateCertificate(rand.Reader, template, c.ca.cert, &key.PublicKey, c.ca.key)
	if err != nil {
		return nil, fmt.Errorf("signing certificate: %w", err)
	}

	return &tls.Certificate{
		Certificate: [][]byte{certDER, c.ca.cert.Raw},
		PrivateKey:  key,
	}, nil
}

// moveToEnd moves a host to the end of the LRU order.
func (c *CertCache) moveToEnd(host string) {
	// Find and remove from current position
	for i, h := range c.order {
		if h == host {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	// Add to end
	c.order = append(c.order, host)
}

// evictOldest removes the oldest (least recently used) entry.
func (c *CertCache) evictOldest() {
	if len(c.order) == 0 {
		return
	}
	oldest := c.order[0]
	c.order = c.order[1:]
	delete(c.cache, oldest)
}

// Size returns the current cache size.
func (c *CertCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.cache)
}

// Clear empties the cache.
func (c *CertCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[string]*cacheEntry)
	c.order = make([]string, 0, c.maxSize)
}
