// Package proxy provides a transparent in-flight request proxy for Antigravity CLI.
package proxy

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"sync"
	"time"

	"github.com/MiloudiMohamed/agy-account-rotator/internal/store"
)

// CertManager manages the local CA and dynamically mints leaf TLS certificates.
type CertManager struct {
	caCert *x509.Certificate
	caKey  *rsa.PrivateKey
	mu     sync.RWMutex
	cache  map[string]*tls.Certificate
}

// LoadOrCreateCA loads the CA certificate and private key from store, or generates fresh ones.
func LoadOrCreateCA(s *store.Store) (*CertManager, error) {
	caPath := s.CAPath()
	caKeyPath := s.CAKeyPath()

	if _, err := os.Stat(caPath); err == nil {
		if _, err := os.Stat(caKeyPath); err == nil {
			certPEM, err := os.ReadFile(caPath)
			if err == nil {
				keyPEM, err := os.ReadFile(caKeyPath)
				if err == nil {
					cm, err := parseCA(certPEM, keyPEM)
					if err == nil {
						return cm, nil
					}
				}
			}
		}
	}

	// Generate new CA
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generating CA private key: %w", err)
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, fmt.Errorf("generating CA serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "agy-rotator Local Intercept CA",
			Organization: []string{"agy-rotator"},
		},
		NotBefore:             time.Now().Add(-10 * time.Minute),
		NotAfter:              time.Now().AddDate(10, 0, 0), // 10 years
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("creating CA certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(caKey)})

	if err := store.WriteFileAtomic(caPath, certPEM, 0o644); err != nil {
		return nil, fmt.Errorf("saving CA certificate: %w", err)
	}
	if err := store.WriteFileAtomic(caKeyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("saving CA private key: %w", err)
	}

	caCert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return nil, fmt.Errorf("parsing generated CA certificate: %w", err)
	}

	return &CertManager{
		caCert: caCert,
		caKey:  caKey,
		cache:  make(map[string]*tls.Certificate),
	}, nil
}

func parseCA(certPEM, keyPEM []byte) (*CertManager, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("failed to parse CA certificate PEM")
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing CA certificate: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("failed to parse CA private key PEM")
	}
	caKey, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	if err != nil {
		// Try PKCS8
		pk, perr := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if perr != nil {
			return nil, fmt.Errorf("parsing CA private key: %w", err)
		}
		var ok bool
		caKey, ok = pk.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("CA private key is not RSA")
		}
	}

	return &CertManager{
		caCert: caCert,
		caKey:  caKey,
		cache:  make(map[string]*tls.Certificate),
	}, nil
}

// GetCertificate dynamically mints or returns a cached TLS certificate for a target hostname.
func (cm *CertManager) GetCertificate(host string) (*tls.Certificate, error) {
	cm.mu.RLock()
	if cert, ok := cm.cache[host]; ok {
		cm.mu.RUnlock()
		return cert, nil
	}
	cm.mu.RUnlock()

	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Double-check under write lock
	if cert, ok := cm.cache[host]; ok {
		return cert, nil
	}

	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generating leaf key: %w", err)
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, fmt.Errorf("generating leaf serial: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   host,
			Organization: []string{"agy-rotator Intercepted"},
		},
		NotBefore:             time.Now().Add(-10 * time.Minute),
		NotAfter:              time.Now().AddDate(1, 0, 0), // 1 year
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, cm.caCert, &leafKey.PublicKey, cm.caKey)
	if err != nil {
		return nil, fmt.Errorf("creating leaf certificate: %w", err)
	}

	tlsCert := &tls.Certificate{
		Certificate: [][]byte{derBytes, cm.caCert.Raw},
		PrivateKey:  leafKey,
	}

	cm.cache[host] = tlsCert
	return tlsCert, nil
}

// TLSConfig returns a tls.Config suitable for tls.Server using SNI dynamic certificates.
func (cm *CertManager) TLSConfig() *tls.Config {
	return &tls.Config{
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			serverName := hello.ServerName
			if serverName == "" {
				serverName = "cloudcode-pa.googleapis.com"
			}
			return cm.GetCertificate(serverName)
		},
	}
}
