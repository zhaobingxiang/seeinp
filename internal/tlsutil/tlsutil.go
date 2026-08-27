package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"
)

// GenerateCert generates a self-signed TLS certificate
func GenerateCert(certFile, keyFile string) (fingerprint string, err error) {
	// Generate private key
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}

	// Create certificate template
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", fmt.Errorf("generate serial: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"seeinp"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour), // 10 years
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// Add localhost to SANs
	template.DNSNames = []string{"localhost", "127.0.0.1", "::1"}
	template.IPAddresses = []net.IP{
		net.ParseIP("127.0.0.1"),
		net.ParseIP("::1"),
	}

	// Create certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return "", fmt.Errorf("create certificate: %w", err)
	}

	// Calculate fingerprint
	fingerprintHash := sha256.Sum256(certDER)
	fingerprint = hex.EncodeToString(fingerprintHash[:])

	// Write certificate
	certFile2, err := os.Create(certFile)
	if err != nil {
		return "", fmt.Errorf("create cert file: %w", err)
	}
	defer certFile2.Close()

	if err := pem.Encode(certFile2, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return "", fmt.Errorf("write cert: %w", err)
	}

	// Write private key
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return "", fmt.Errorf("marshal key: %w", err)
	}

	keyFile2, err := os.Create(keyFile)
	if err != nil {
		return "", fmt.Errorf("create key file: %w", err)
	}
	defer keyFile2.Close()

	if err := pem.Encode(keyFile2, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		return "", fmt.Errorf("write key: %w", err)
	}

	return fingerprint, nil
}

// LoadCert loads TLS certificate from files
func LoadCert(certFile, keyFile string) (tls.Certificate, error) {
	return tls.LoadX509KeyPair(certFile, keyFile)
}

// Fingerprint calculates SHA256 fingerprint of a TLS certificate
func Fingerprint(cert tls.Certificate) string {
	hash := sha256.Sum256(cert.Certificate[0])
	return hex.EncodeToString(hash[:])
}
