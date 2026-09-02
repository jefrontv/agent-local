package main

import "testing"

// wp-cli output must be wp-cli's: PHP's compile-time deprecations from the
// phar and plugins' runtime warnings are dropped, while wp-cli's own Error
// and Warning lines, and ordinary output, come through untouched.
func TestStripPHPNoiseDropsRuntimeDiagnosticsOnly(t *testing.T) {
	in := "\nDeprecated: Case statements followed by a semicolon (;) are deprecated, use a colon (:) instead in phar:///Users/x/.agent-local/bin/wp/vendor/react/promise/src/functions.php on line 369\n" +
		"\nWarning: Cannot modify header information - headers already sent by (output started at phar:///x/functions_include.php:4) in /Users/x/wp-content/plugins/handl/handl.php on line 58\n" +
		"PHP Notice:  Undefined index: foo in /srv/wp-content/themes/t/functions.php on line 12\n" +
		"Warning: Plugin 'gravityformsrecaptcha' not found.\n" +
		"Success: Deactivated 1 of 1 plugins.\n" +
		"Error: The site is not installed.\n"
	want := "Warning: Plugin 'gravityformsrecaptcha' not found.\n" +
		"Success: Deactivated 1 of 1 plugins.\n" +
		"Error: The site is not installed.\n"
	if got := stripPHPNoise(in); got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestStripPHPNoiseLeavesCleanOutputAlone(t *testing.T) {
	in := "6.6.2\n"
	if got := stripPHPNoise(in); got != in {
		t.Fatalf("clean output changed: %q", got)
	}
	if got := stripPHPNoise(""); got != "" {
		t.Fatalf("empty changed: %q", got)
	}
}
