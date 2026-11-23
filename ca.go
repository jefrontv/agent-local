package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The local CA is what makes a new site's certificate trusted without a
// password. Every site cert used to be its own self-signed root, so each one
// was a separate keychain write, and on current macOS a trust-settings change
// can ask for authorization even when it runs as root. With one CA trusted
// once, a leaf it signs chains to an anchor the system already has: nothing
// is written to the keychain, so nothing asks.
//
// The CA's key on disk could sign for any name the CA allows, so the CA is
// name-constrained to the suffixes agent-local hands out. A domain outside
// that set (a custom domain, a suffix switched to after the CA was made)
// gets a self-signed cert and the per-site trust it always had.

// caBaseDomains are always permitted: the default suffix and the reserved
// local-only names people set suffixes and custom domains to.
var caBaseDomains = []string{strings.TrimPrefix(DefaultSuffix, "."), "test", "local", "localhost"}

// caPaths are the CA's files. The bundle holds cert and key together so one
// atomic link creates both; the .crt beside it is the public half, the file
// the keychain is pointed at. A subdirectory keeps them clear of the
// per-domain <domain>.crt names.
func caPaths() (bundle, cert string) {
	d := filepath.Join(P().Certs(), "ca")
	return filepath.Join(d, AppName+"-ca.pem"), filepath.Join(d, AppName+"-ca.crt")
}

type localCA struct {
	cert     *x509.Certificate
	key      *ecdsa.PrivateKey
	certPath string
}

// permits mirrors x509 name-constraint matching for a DNS name: a permitted
// "al" covers "al" and anything under ".al".
func (ca *localCA) permits(domain string) bool {
	d := strings.ToLower(strings.TrimSuffix(domain, "."))
	for _, c := range ca.cert.PermittedDNSDomains {
		c = strings.ToLower(strings.TrimPrefix(c, "."))
		if d == c || strings.HasSuffix(d, "."+c) {
			return true
		}
	}
	return false
}

var (
	caMu          sync.Mutex
	caCache       *localCA
	caBundleMtime time.Time
	caCacheAt     string // bundle path the cache was loaded from; tests move $HOME
)

// loadCA returns the local CA, creating it first when create is set and none
// exists. Creation is safe across processes: the bundle is written to a temp
// file and linked into place, so when the CLI and the daemon race, one link
// wins and both load the winner.
func loadCA(create bool) (*localCA, error) {
	caMu.Lock()
	defer caMu.Unlock()
	bundle, certPath := caPaths()
	st, statErr := os.Stat(bundle)
	if caCache != nil && caCacheAt == bundle && statErr == nil && caBundleMtime.Equal(st.ModTime()) {
		return caCache, nil
	}
	b, err := os.ReadFile(bundle)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil, err
		}
		if err := createCABundle(bundle); err != nil {
			return nil, err
		}
		b, err = os.ReadFile(bundle)
	}
	if err != nil {
		return nil, err
	}
	ca, certPEM, err := parseCABundle(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", bundle, err)
	}
	if err := writePublicCert(certPath, certPEM); err != nil {
		return nil, err
	}
	ca.certPath = certPath
	if statErr == nil {
		caBundleMtime = st.ModTime()
	} else {
		caBundleMtime = time.Time{}
	}
	caCache, caCacheAt = ca, bundle
	return ca, nil
}

// createCABundle generates a CA and links it to bundle unless one is already
// there. Losing the race is success: the caller reads whatever won.
func createCABundle(bundle string) error {
	if err := os.MkdirAll(filepath.Dir(bundle), 0o700); err != nil {
		return err
	}
	pemBytes, err := generateCA(caPermittedDomains())
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(bundle), ".ca-*.pem")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(pemBytes); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// CreateTemp makes the file 0600, which is what a CA key needs.
	if err := os.Link(tmp.Name(), bundle); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

// caPermittedDomains is the base set plus the configured suffix, read from
// the store file directly: EnsureCert has no store in hand, and opening one
// here would create directories as a side effect.
func caPermittedDomains() []string {
	out := append([]string(nil), caBaseDomains...)
	b, err := os.ReadFile(P().Store())
	if err != nil {
		return out
	}
	var data struct {
		Suffix string `json:"domain_suffix"`
	}
	if json.Unmarshal(b, &data) != nil {
		return out
	}
	sfx := strings.ToLower(strings.TrimPrefix(data.Suffix, "."))
	if sfx == "" {
		return out
	}
	for _, d := range out {
		if d == sfx {
			return out
		}
	}
	return append(out, sfx)
}

// generateCA returns a PEM bundle: the CA certificate, then its key.
func generateCA(permitted []string) ([]byte, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	// The user and host in the name are what someone reads in Keychain Access,
	// and keep two machines' CAs from looking like the same thing.
	owner := os.Getenv("USER")
	if h, err := os.Hostname(); err == nil {
		owner += "@" + strings.TrimSuffix(h, ".local")
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:         AppName + " local CA " + owner,
			Organization:       []string{AppName},
			OrganizationalUnit: []string{owner},
		},
		NotBefore: time.Now().Add(-time.Hour),
		// Roots are exempt from the 398-day leaf cap. Ten years means the one
		// trust prompt is not something anyone sees again.
		NotAfter:                    time.Now().AddDate(10, 0, 0),
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid:       true,
		IsCA:                        true,
		MaxPathLenZero:              true,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         permitted,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}
	keyDer, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	pem.Encode(&buf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDer})
	return buf.Bytes(), nil
}

// parseCABundle reads the cert and key back and checks they belong together,
// so a damaged bundle fails here rather than signing leaves nothing trusts.
func parseCABundle(b []byte) (*localCA, []byte, error) {
	var ca localCA
	var certPEM []byte
	for {
		var block *pem.Block
		block, b = pem.Decode(b)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			c, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, nil, err
			}
			ca.cert, certPEM = c, pem.EncodeToMemory(block)
		case "EC PRIVATE KEY":
			k, err := x509.ParseECPrivateKey(block.Bytes)
			if err != nil {
				return nil, nil, err
			}
			ca.key = k
		}
	}
	if ca.cert == nil || ca.key == nil {
		return nil, nil, errors.New("CA bundle is missing its certificate or key")
	}
	if !ca.cert.IsCA {
		return nil, nil, errors.New("CA bundle certificate is not a CA")
	}
	if !ca.key.PublicKey.Equal(ca.cert.PublicKey) {
		return nil, nil, errors.New("CA bundle key does not match its certificate")
	}
	return &ca, certPEM, nil
}

// writePublicCert keeps the public .crt in step with the bundle, rewriting it
// through a rename only when it is missing or differs.
func writePublicCert(path string, certPEM []byte) error {
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, certPEM) {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ca-*.crt")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(certPEM); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// signerFor picks who signs a new leaf for domain: the local CA when it
// covers the name, otherwise nil and the leaf signs itself. A CA that cannot
// be loaded or made is not an error here; the old per-site path still works.
func signerFor(domain string) *localCA {
	ca, err := loadCA(true)
	if err != nil || !ca.permits(domain) {
		return nil
	}
	return ca
}

// trustAnchorFor is the certificate to put in the keychain so certPath is
// trusted: our CA when it issued certPath, else certPath itself.
func trustAnchorFor(certPath string) string {
	ca, err := loadCA(false)
	if err != nil {
		return certPath
	}
	b, err := os.ReadFile(certPath)
	if err != nil {
		return certPath
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return certPath
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil || leaf.CheckSignatureFrom(ca.cert) != nil {
		return certPath
	}
	return ca.certPath
}

// CAStatus reports the local CA for doctor and the API.
type CAStatus struct {
	Exists    bool     `json:"exists"`
	Trusted   bool     `json:"trusted"`
	CertPath  string   `json:"cert_path,omitempty"`
	NotAfter  string   `json:"not_after,omitempty"`
	Permitted []string `json:"permitted,omitempty"`
}

// InspectCA reads the CA's state without creating it.
func InspectCA() CAStatus {
	ca, err := loadCA(false)
	if err != nil {
		return CAStatus{}
	}
	return CAStatus{
		Exists:    true,
		Trusted:   caTrusted(ca),
		CertPath:  ca.certPath,
		NotAfter:  ca.cert.NotAfter.Format(time.RFC3339),
		Permitted: ca.cert.PermittedDNSDomains,
	}
}

// caTrusted asks the OS whether the CA is a trusted SSL anchor.
func caTrusted(ca *localCA) bool {
	return certTrusted(ca.certPath)
}

// TrustLocalCA creates the CA if needed and trusts it: the one keychain write
// that makes every later site cert trusted with no prompt.
func TrustLocalCA(interactive bool) error {
	ca, err := loadCA(true)
	if err != nil {
		return err
	}
	return trustAnchor(ca.certPath, ca.certPath, interactive)
}
