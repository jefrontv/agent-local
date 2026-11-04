package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func caTestHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(P().Certs(), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readLeaf(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		t.Fatalf("%s: no PEM block", path)
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A site under a permitted suffix gets a leaf the CA signed, and that leaf
// verifies for its name against a pool holding only the CA: what the browser
// does once the CA is in the keychain.
func TestEnsureCertSignsWithLocalCA(t *testing.T) {
	caTestHome(t)
	cert, _, created, err := EnsureCert("shop.al")
	if err != nil || !created {
		t.Fatalf("EnsureCert: created=%v err=%v", created, err)
	}
	ca, err := loadCA(false)
	if err != nil {
		t.Fatalf("CA not created alongside the first leaf: %v", err)
	}
	leaf := readLeaf(t, cert)
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "shop.al", Roots: pool}); err != nil {
		t.Fatalf("leaf does not verify against the CA: %v", err)
	}
	if got := trustAnchorFor(cert); got != ca.certPath {
		t.Errorf("trust anchor = %s, want the CA %s", got, ca.certPath)
	}
	if leaf.NotAfter.Sub(leaf.NotBefore) > 399*24*time.Hour {
		t.Errorf("leaf lifetime %v exceeds the 398-day cap", leaf.NotAfter.Sub(leaf.NotBefore))
	}
}

// A name the CA may not sign keeps the old self-signed path, so a custom
// domain still works through its own trust step.
func TestEnsureCertSelfSignsOutsideCA(t *testing.T) {
	caTestHome(t)
	cert, _, _, err := EnsureCert("client.dev")
	if err != nil {
		t.Fatal(err)
	}
	leaf := readLeaf(t, cert)
	// CheckSignatureFrom insists the parent is a CA, which a self-signed leaf
	// is not; check the signature against its own key directly.
	if err := leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature); err != nil {
		t.Fatalf("leaf outside the CA should be self-signed: %v", err)
	}
	if got := trustAnchorFor(cert); got != cert {
		t.Errorf("trust anchor = %s, want the leaf itself", got)
	}
}

// The name constraint is what limits a stolen CA key. Sign a leaf for a name
// outside it by hand and the verifier must refuse it.
func TestLocalCANameConstraintHolds(t *testing.T) {
	caTestHome(t)
	ca, err := loadCA(true)
	if err != nil {
		t.Fatal(err)
	}
	if !ca.cert.PermittedDNSDomainsCritical {
		t.Error("name constraint is not critical")
	}
	for name, want := range map[string]bool{
		"x.al": true, "a.b.test": true, "localhost": true, "x.local": true,
		"bank.com": false, "x.dev": false, "notal": false,
	} {
		if got := ca.permits(name); got != want {
			t.Errorf("permits(%q) = %v, want %v", name, got, want)
		}
	}

	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: "bank.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"bank.com"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, ca.cert, &priv.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "bank.com", Roots: pool}); err == nil {
		t.Fatal("a leaf for bank.com verified against the local CA")
	}
}

// The CA is made once. A second load, and a load by another process (no
// in-memory cache), see the same CA; the key stays private.
func TestLocalCAIsStable(t *testing.T) {
	caTestHome(t)
	first, err := loadCA(true)
	if err != nil {
		t.Fatal(err)
	}
	caMu.Lock()
	caCache = nil
	caMu.Unlock()
	second, err := loadCA(true)
	if err != nil {
		t.Fatal(err)
	}
	if first.cert.SerialNumber.Cmp(second.cert.SerialNumber) != 0 {
		t.Fatal("a second load made a new CA")
	}
	bundle, crt := caPaths()
	if st, _ := os.Stat(bundle); st.Mode().Perm() != 0o600 {
		t.Errorf("bundle mode = %v, want 0600", st.Mode().Perm())
	}
	if st, _ := os.Stat(crt); st.Mode().Perm() != 0o644 {
		t.Errorf("public cert mode = %v, want 0644", st.Mode().Perm())
	}
	if c := readLeaf(t, crt); c.SerialNumber.Cmp(first.cert.SerialNumber) != 0 {
		t.Error("public cert does not match the bundle")
	}
}

// The CLI and the daemon can both reach for the CA first. Whoever links the
// bundle wins and the other must not overwrite it.
func TestCreateCABundleRace(t *testing.T) {
	caTestHome(t)
	bundle, _ := caPaths()
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = createCABundle(bundle)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseCABundle(b); err != nil {
		t.Fatalf("bundle after race: %v", err)
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(bundle), ".ca-*"))
	if len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

// A suffix configured before the CA exists is covered by it.
func TestCAPermitsConfiguredSuffix(t *testing.T) {
	caTestHome(t)
	if err := os.WriteFile(P().Store(), []byte(`{"domain_suffix":".jake"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ca, err := loadCA(true)
	if err != nil {
		t.Fatal(err)
	}
	if !ca.permits("site.jake") {
		t.Errorf("configured suffix not permitted: %v", ca.cert.PermittedDNSDomains)
	}
}
