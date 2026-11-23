package main

import (
	"crypto/sha256"
	"fmt"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"os"
	"regexp"
)

func verifyReleaseDownload(archive, asset, sumsURL, tag string) error {
	sums, err := downloadTemp(sumsURL, "agent-local-checksums-*")
	if err != nil {
		return err
	}
	defer os.Remove(sums)
	signature, err := downloadTemp(sumsURL+".sigstore.json", "agent-local-signature-*")
	if err != nil {
		return err
	}
	defer os.Remove(signature)
	return verifyReleaseFiles(archive, asset, sums, signature, tag)
}

var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?$`)

func verifyReleaseFiles(archive, asset, sums, signature, tag string) error {
	if !releaseTagPattern.MatchString(tag) {
		return fmt.Errorf("invalid release tag %q", tag)
	}
	signed, err := bundle.LoadJSONFromPath(signature)
	if err != nil {
		return fmt.Errorf("read release signature: %w", err)
	}
	manifest, err := os.ReadFile(sums)
	if err != nil {
		return err
	}
	identity, err := verify.NewShortCertificateIdentity("https://token.actions.githubusercontent.com", "", "https://github.com/"+updateRepo+"/.github/workflows/release.yml@refs/tags/"+tag, "")
	if err != nil {
		return err
	}
	client, err := tuf.New(tuf.DefaultOptions())
	if err != nil {
		return err
	}
	trusted, err := root.GetTrustedRoot(client)
	if err != nil {
		return err
	}
	verifier, err := verify.NewVerifier(trusted, verify.WithSignedCertificateTimestamps(1), verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		return err
	}
	digest := sha256.Sum256(manifest)
	_, err = verifier.Verify(signed, verify.NewPolicy(verify.WithArtifactDigest("sha256", digest[:]), verify.WithCertificateIdentity(identity)))
	if err != nil {
		return fmt.Errorf("verify release %s: %w", tag, err)
	}
	return checkArchiveChecksum(archive, asset, manifest)
}
