package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// CertPaths for a domain: key + cert under Root/certs.
func CertPaths(domain string) (cert, key string) {
	p := P().Certs()
	return filepath.Join(p, domain+".crt"), filepath.Join(p, domain+".key")
}

// EnsureCert generates (once) a self-signed cert for the domain.
func EnsureCert(domain string) (cert, key string, created bool, err error) {
	cert, key = CertPaths(domain)
	if fileExists(cert) && fileExists(key) {
		return cert, key, false, nil
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", false, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: domain, Organization: []string{AppName}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{domain},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return "", "", false, err
	}
	keyDer, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return "", "", false, err
	}
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return "", "", false, err
	}
	if err := os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDer}), 0o600); err != nil {
		return "", "", false, err
	}
	return cert, key, true, nil
}

// TrustCert adds our cert to the system keychain as trusted for SSL
// (macOS only; interactive). Returns nil if already trusted.
func TrustCert(certPath string, interactive bool) error {
	if !fileExists(certPath) {
		return fmt.Errorf("cert missing: %s", certPath)
	}
	out, err := runCmdOut("security", "find-certificate", "-c", AppName, "/Library/Keychains/System.keychain")
	if err == nil && out != "" {
		return nil
	}
	return RunPrivileged(interactive, "security", "add-trusted-cert", "-d", "-r", "trustRoot",
		"-p", "ssl", "-k", "/Library/Keychains/System.keychain", certPath)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
