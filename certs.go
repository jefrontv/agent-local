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
	"strings"
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
		// 398 days: the max lifetime modern browsers/OSes accept for a
		// leaf cert (Apple/Chrome cap at 397). A 10-year cert used to
		// outlive that policy and start failing TLS handshakes silently.
		NotAfter:    time.Now().Add(398 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{domain},
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

// trustStagePath is the one path the sudoers allowlist permits
// `add-trusted-cert` on. Root-owned (written through `tee` as root), fixed,
// and outside any user-writable directory, so the allowlist entry cannot be
// pointed at an arbitrary certificate. /var/db is root-only and survives
// reboots; /tmp would let another user pre-create the name.
const trustStagePath = "/var/db/agent-local-trust.crt"

// TrustCert adds our cert to the system keychain as trusted for SSL
// (macOS only). No-op when the OS already trusts it.
//
// Silent path first: stage the PEM to trustStagePath through `sudo -n tee`
// (stdin, so the bytes never sit in a user-writable file), then trust that
// exact path. Both steps are on the allowlist `agent-local sudo` installs,
// which is what lets the daemon — with nowhere to show a password prompt —
// trust a new site's cert. Without the allowlist, an interactive caller gets
// the GUI dialog against the cert's real path; a non-interactive one fails
// with the setup hint. Success is what the OS says afterwards, never the
// command's exit code: a cancelled dialog exits 0 on some macOS builds.
func TrustCert(certPath string, interactive bool) error {
	if !fileExists(certPath) {
		return fmt.Errorf("cert missing: %s", certPath)
	}
	if certTrusted(certPath) {
		return nil
	}
	der, err := os.ReadFile(certPath)
	if err != nil {
		return err
	}
	if sudoNStdin(der, "/usr/bin/tee", trustStagePath) == nil &&
		sudoN("-n", "/usr/bin/security", "add-trusted-cert", "-d", "-r", "trustRoot",
			"-p", "ssl", "-k", "/Library/Keychains/System.keychain", trustStagePath) == nil {
		if certTrusted(certPath) {
			return nil
		}
	}
	if !interactive {
		return fmt.Errorf("needs root to trust %s (run: agent-local sudo, or: agent-local cert %s --trust)", filepath.Base(certPath), strings.TrimSuffix(filepath.Base(certPath), ".crt"))
	}
	if err := RunPrivileged(true, "security", "add-trusted-cert", "-d", "-r", "trustRoot",
		"-p", "ssl", "-k", "/Library/Keychains/System.keychain", certPath); err != nil {
		return err
	}
	if !certTrusted(certPath) {
		return fmt.Errorf("the password prompt was cancelled or the keychain refused; %s is still untrusted", filepath.Base(certPath))
	}
	return nil
}

// certTrusted asks the OS whether it trusts this exact certificate for SSL.
// The old check looked for a common name of "agent-local", which our certs
// never carry (CN is the domain), so every issue re-wrote the keychain.
func certTrusted(certPath string) bool {
	_, err := runCmdOut("security", "verify-cert", "-c", certPath, "-p", "ssl")
	return err == nil
}

// CertStatus is what an integrator needs to show a padlock: does a cert exist
// for this domain, is the system keychain trusting it, and when does it lapse.
type CertStatus struct {
	Domain   string `json:"domain"`
	CertPath string `json:"cert_path"`
	KeyPath  string `json:"key_path"`
	Exists   bool   `json:"exists"`
	Trusted  bool   `json:"trusted"`
	NotAfter string `json:"not_after,omitempty"`
	Issuer   string `json:"issuer,omitempty"`
}

// InspectCert reports the trust state of a domain's certificate. Trust is read
// from the OS, not assumed from the fact that we issued the cert: a user can
// revoke it in Keychain Access at any time, and a stale "trusted" badge would
// send them hunting for a bug in the wrong place.
func InspectCert(domain string) CertStatus {
	certPath, keyPath := CertPaths(domain)
	st := CertStatus{Domain: domain, CertPath: certPath, KeyPath: keyPath}
	b, err := os.ReadFile(certPath)
	if err != nil {
		return st
	}
	st.Exists = true
	block, _ := pem.Decode(b)
	if block == nil {
		return st
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return st
	}
	st.NotAfter = parsed.NotAfter.Format(time.RFC3339)
	st.Issuer = parsed.Issuer.CommonName
	// `verify-cert -p ssl` walks the real trust settings, so a cert the user
	// untrusted reports false even though the file is still on disk.
	_, verr := runCmdOut("security", "verify-cert", "-c", certPath, "-p", "ssl")
	st.Trusted = verr == nil
	return st
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
