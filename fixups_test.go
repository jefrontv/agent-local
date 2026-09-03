package main

import (
	"strings"
	"testing"
)

// Hyphenated slugs are the norm ("my-site" -> DB "al_my-site"), so identifier
// validation must accept hyphens while still refusing anything that can break
// out of backtick quoting or a single-quoted user literal.
func TestSQLIdentAllowsHyphenatedNames(t *testing.T) {
	for _, ok := range []string{"al_my-site", "al_demo", "wp_2options", "a$B_c-d"} {
		if err := requireSQLIdent("database name", ok); err != nil {
			t.Errorf("requireSQLIdent(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "a`b", "a'b", "a;b", "a b", "a/b", "a\\b", "db.name"} {
		if err := requireSQLIdent("database name", bad); err == nil {
			t.Errorf("requireSQLIdent(%q) = nil, want error", bad)
		}
	}
}

// Worktree ids are slug + "--" + branch-slug; neither half can contain "--",
// so the first separator is unambiguous. Anything else is a caller bug and
// must error instead of routing against a wrong or empty site slug.
func TestWorktreeSiteSlug(t *testing.T) {
	if site, ok := worktreeSiteSlug("shop--main"); !ok || site != "shop" {
		t.Errorf("worktreeSiteSlug(shop--main) = %q, %v", site, ok)
	}
	for _, bad := range []string{"", "noseparator", "--main", "shop--", "--"} {
		if site, ok := worktreeSiteSlug(bad); ok {
			t.Errorf("worktreeSiteSlug(%q) = %q, true; want rejection", bad, site)
		}
	}
}

// A non-string element in an args array is a caller bug. It used to be
// silently dropped, so a wp-cli call ran with fewer arguments than asked for
// and reported success.
func TestStrArgsRejectsNonString(t *testing.T) {
	if _, err := strArgs([]interface{}{"a", 1}); err == nil {
		t.Error("strArgs([a, 1]) = nil error, want type error")
	}
	if out, err := strArgs([]interface{}{"a"}); err != nil || len(out) != 1 {
		t.Errorf("strArgs([a]) = %v, %v", out, err)
	}
	if out, err := strArgs(nil); err != nil || len(out) != 0 {
		t.Errorf("strArgs(nil) = %v, %v", out, err)
	}
}

// A tools/call missing a required argument must fail with the argument named,
// not build a request against an empty path segment and return a confusing 404.
func TestValidateRequiredNamesMissingArg(t *testing.T) {
	var tool *mcpTool
	for i, tl := range mcpTools() {
		if tl.Name == "get_site" {
			tool = &mcpTools()[i]
		}
	}
	if tool == nil {
		t.Fatal("get_site tool missing")
	}
	if verr := validateRequired(*tool, map[string]interface{}{}); verr == nil ||
		verr.Code != -32602 || !strings.Contains(verr.Message, "slug") {
		t.Errorf("validateRequired({}) = %+v, want -32602 naming slug", verr)
	}
	if verr := validateRequired(*tool, map[string]interface{}{"slug": "s"}); verr != nil {
		t.Errorf("validateRequired({slug}) = %+v, want nil", verr)
	}
}

// The sendmail id lands in a filesystem path. Pool ids are internal, but the
// flag arrives via argv, so traversal shapes must fail closed.
func TestStoreMailRejectsTraversalIDs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, bad := range []string{"", "../x", "a/b", `a\b`, "."} {
		if _, err := StoreMail(bad, []byte("x")); err == nil {
			t.Errorf("StoreMail(%q) = nil error, want rejection", bad)
		}
	}
}

// Subprocess output is rendered into the user's terminal. Escape sequences
// must be stripped so a chatty tool cannot repaint lines or fake a prompt.
func TestSanitizeSubOutputStripsEscapes(t *testing.T) {
	in := "\x1b[31mred\x1b[0m plain \x1b]0;title\x07 keep\tme"
	got := sanitizeSubOutput(in)
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("sanitizeSubOutput left escape bytes: %q", got)
	}
	for _, want := range []string{"red", "plain", "keep\tme"} {
		if !strings.Contains(got, want) {
			t.Errorf("sanitizeSubOutput(%q) lost %q", in, want)
		}
	}
}
