package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// EnsureWPCLI downloads the wp-cli phar to our bin dir (once).
func EnsureWPCLI() (string, error) {
	bin := filepath.Join(P().Bin(), "wp")
	if fileExists(bin) {
		return bin, nil
	}
	tmp, err := os.CreateTemp("", "wp-cli-*.phar")
	if err != nil {
		return "", err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if err := runCmdQuiet("curl", "-fsSL", "-o", tmp.Name(), "https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar"); err != nil {
		return "", fmt.Errorf("download wp-cli: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), bin); err != nil {
		return "", err
	}
	return bin, nil
}

// wpCLI runs wp-cli against a site using that site's PHP runtime.
func wpCLI(site *Site, args ...string) (string, error) {
	return wpCLIAt(site, site.WPDir, args...)
}

// wpCLIAt runs wp-cli against an explicit docroot (a worktree preview) while
// keeping the site's PHP version and DB.
func wpCLIAt(site *Site, dir string, args ...string) (string, error) {
	bin, err := EnsureWPCLI()
	if err != nil {
		return "", err
	}
	php := "php"
	if s, err := OpenStore(); err == nil {
		if rt := s.Inventory().FindPHP(site.PHPVersion); rt != nil {
			php = rt.Bin
		}
	}
	full := append([]string{bin, "--path=" + dir}, args...)
	cmd := exec.Command(php, full...)
	cmd.Env = append(os.Environ(), "WP_CLI_CACHE_DIR="+filepath.Join(P().Root, "wp-cli-cache"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}
