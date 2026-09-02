package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
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
//
// The output is wp-cli's, not PHP's: the phar trips deprecations on new PHP
// releases and plugins emit warnings on load, and none of that is the answer
// to the command that was asked. PHP is told to put its diagnostics on stderr
// (WordPress switches display_errors back on at runtime, so that alone is
// not enough), and both streams are then stripped of PHP-runtime lines.
// wp-cli's own "Error:" and "Warning:" lines survive, as does the exit code.
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
	full := append([]string{"-d", "display_errors=stderr", bin, "--path=" + dir}, args...)
	cmd := exec.Command(php, full...)
	cmd.Env = append(os.Environ(), "WP_CLI_CACHE_DIR="+filepath.Join(P().Root, "wp-cli-cache"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	out := stripPHPNoise(stdout.String())
	if e := stripPHPNoise(stderr.String()); e != "" {
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += e
	}
	return out, err
}

// phpNoise matches one PHP runtime diagnostic as the CLI SAPI prints it:
// "Deprecated: ... in /path/file.php on line 12", with or without the "PHP "
// prefix the stderr/log format adds. wp-cli's own messages never carry the
// "in <file> on line <n>" tail, so they are untouched.
var phpNoise = regexp.MustCompile(`^(?:PHP )?(?:Deprecated|Warning|Notice|Strict Standards):\s.*\bon line \d+\s*$`)

// stripPHPNoise drops PHP diagnostic lines and the blank line PHP prints
// ahead of each one ("\nWarning: …"), leaving everything else in order.
func stripPHPNoise(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		if !phpNoise.MatchString(strings.TrimSpace(l)) {
			kept = append(kept, l)
			continue
		}
		if n := len(kept); n > 0 && strings.TrimSpace(kept[n-1]) == "" {
			kept = kept[:n-1]
		}
	}
	return strings.Join(kept, "\n")
}
