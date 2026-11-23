package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CertPaths for a domain: key + cert under Root/certs.
func CertPaths(domain string) (cert, key string) {
	p := P().Certs()
	return filepath.Join(p, domain+".crt"), filepath.Join(p, domain+".key")
}

type keyedLocks struct{ entries sync.Map }

func (k *keyedLocks) lock(id string) func() {
	v, _ := k.entries.LoadOrStore(id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

var certLocks, shareStartLocks, poolActivityLocks keyedLocks

// EnsureCert generates (once) a cert for the domain: signed by the local CA
// when the CA covers the name (ca.go), self-signed otherwise.
func EnsureCert(domain string) (cert, key string, created bool, err error) {
	cert, key = CertPaths(domain)
	unlock := certLocks.lock(cert)
	defer unlock()
	if pair, err := tls.LoadX509KeyPair(cert, key); err == nil {
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err == nil && leaf.VerifyHostname(domain) == nil && time.Until(leaf.NotAfter) > 30*24*time.Hour {
			return cert, key, false, nil
		}
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
		// Leave room for the backdated start within the browser lifetime limit.
		NotAfter:    time.Now().Add(396 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{domain},
	}
	parent, signer := &tmpl, crypto.Signer(priv)
	if ca := signerFor(domain); ca != nil {
		parent, signer = ca.cert, ca.key
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, parent, &priv.PublicKey, signer)
	if err != nil {
		return "", "", false, err
	}
	keyDer, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return "", "", false, err
	}
	if err := atomicWriteMode(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return "", "", false, err
	}
	if err := atomicWriteMode(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDer}), 0o600); err != nil {
		return "", "", false, err
	}
	return cert, key, true, nil
}

// TrustCert verifies trust after the system keychain prompt.
func TrustCert(certPath string, interactive bool) error {
	if !fileExists(certPath) {
		return fmt.Errorf("cert missing: %s", certPath)
	}
	if certTrusted(certPath) {
		return nil
	}
	return trustAnchor(trustAnchorFor(certPath), certPath, interactive)
}

// trustAnchor adds anchorPath to the system keychain as an SSL root, then
// confirms that checkPath (the anchor itself, or a leaf it issued) verifies.
func trustAnchor(anchorPath, checkPath string, interactive bool) error {
	if certTrusted(checkPath) {
		return nil
	}
	certPath := anchorPath
	if !interactive {
		if certPath == checkPath && filepath.Base(certPath) == "agent-local-ca.crt" {
			return fmt.Errorf("needs root to trust local CA (run: agent-local setup)")
		}
		return fmt.Errorf("needs root to trust %s (run: agent-local cert %s --trust)", filepath.Base(certPath), strings.TrimSuffix(filepath.Base(checkPath), ".crt"))
	}
	if err := RunPrivileged(true, "/usr/bin/security", "add-trusted-cert", "-d", "-r", "trustRoot",
		"-p", "ssl", "-k", "/Library/Keychains/System.keychain", certPath); err != nil {
		return err
	}
	if !certTrusted(checkPath) {
		return fmt.Errorf("the password prompt was cancelled or the keychain refused; %s is still untrusted", filepath.Base(checkPath))
	}
	return nil
}

// certRemedy is the next step for an untrusted cert, appended to a failure
// that does not already name it — the non-interactive message does, the
// cancelled-prompt and osascript ones do not.
const certRemedy = " (run: agent-local setup)"

// trustCertOrReport trusts a cert we just issued and reports a failure instead
// of dropping it. A setup path is served either way, so a swallowed error here
// surfaces as a browser warning the user reads as a broken site. Trust and
// report are injected so this decision is testable without root; report matches
// the progress callbacks the setup paths already emit.
//
// Reach is bounded by the callers' `created` guard: a cert that already exists
// but is untrusted is served without ever landing here.
func trustCertOrReport(certPath string, interactive bool, trust func(string, bool) error, report func(stage, detail string)) {
	err := trust(certPath, interactive)
	if err == nil {
		return
	}
	msg := err.Error()
	if !strings.Contains(msg, "run: agent-local") {
		msg += certRemedy
	}
	report("warn", "cert not trusted: "+msg)
}

// warnStderr is the reporter for call paths with no progress callback, in the
// shape the rest of those paths already use.
func warnStderr(_, detail string) {
	fmt.Fprintf(os.Stderr, "warning: %s\n", detail)
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
