package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// searchHit is one row of wp-cli search-replace's table output: a table and
// column pair that would be (or was) touched, and how many cells matched.
type searchHit struct {
	Table  string `json:"table"`
	Column string `json:"column"`
	Count  int    `json:"count"`
}

// searchReport is what DBSearch and SearchReplace hand back to the agent.
// Needle/Replacement mirror the arguments given (Replacement is "" for a
// plain search, where old == new). RawTail carries the end of wp-cli's own
// output when the table parser found nothing to show - a summary line format
// wp-cli hasn't shipped yet should never look like "zero matches".
type searchReport struct {
	Needle              string      `json:"needle"`
	Replacement         string      `json:"replacement"`
	DryRun              bool        `json:"dry_run"`
	Hits                []searchHit `json:"hits"`
	Total               int         `json:"total"`
	ConfigPinsRewritten bool        `json:"config_pins_rewritten"`
	RawTail             string      `json:"raw_tail,omitempty"`
}

// searchReplaceTableRowRe matches one data row of wp-cli's --format=table
// output: "| Table | Column | Replacements | Type |". The border rows
// ("+---+---+") and the header row never match this - they start with "|"
// too but the count column parses cleanly only on data rows, so a failed
// strconv.Atoi is what tells the two apart.
var searchReplaceTableRowRe = regexp.MustCompile(`^\|\s*(.+?)\s*\|\s*(.+?)\s*\|\s*(\d+)\s*\|`)

// searchReplaceSuccessRe reads wp-cli's trailing summary line, which comes in
// two forms depending on --dry-run:
//
//	Success: 15 replacements to be made.
//	Success: Made 755 replacements.
var searchReplaceSuccessRe = regexp.MustCompile(`Success:\s*(?:Made\s+)?(\d+)\s+replacements?`)

// parseSearchReplaceTable reads wp-cli search-replace's --format=table
// output into structured hits, dropping zero-count rows (search-replace is
// always run with --report-changed-only so real runs never emit these, but a
// dry run without that guarantee still might). Total prefers the "Success:"
// summary line - it is wp-cli's own count and survives table rows that got
// split or wrapped by a narrow terminal - and only falls back to summing the
// parsed hits when no summary line is present.
func parseSearchReplaceTable(out string) (hits []searchHit, total int) {
	haveSuccess := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := searchReplaceSuccessRe.FindStringSubmatch(line); m != nil {
			total, _ = strconv.Atoi(m[1])
			haveSuccess = true
			continue
		}
		table, column, countText, ok := splitSearchReplaceRow(line)
		if !ok {
			continue
		}
		if strings.EqualFold(table, "Table") && strings.EqualFold(column, "Column") {
			continue // header row
		}
		count, err := strconv.Atoi(countText)
		if err != nil || count == 0 {
			continue
		}
		hits = append(hits, searchHit{Table: table, Column: column, Count: count})
	}
	if !haveSuccess {
		total = 0
		for _, h := range hits {
			total += h.Count
		}
	}
	return hits, total
}

// splitSearchReplaceRow reads one row of --format=table output in either of
// the shapes wp-cli produces: the box-drawn `| a | b | 3 | SQL |` on a
// terminal, and plain tab-separated columns when stdout is a pipe - which is
// what it always is from here.
func splitSearchReplaceRow(line string) (table, column, count string, ok bool) {
	if m := searchReplaceTableRowRe.FindStringSubmatch(line); m != nil {
		return strings.TrimSpace(m[1]), strings.TrimSpace(m[2]), m[3], true
	}
	if strings.Count(line, "\t") >= 2 {
		f := strings.Split(line, "\t")
		return strings.TrimSpace(f[0]), strings.TrimSpace(f[1]), strings.TrimSpace(f[2]), true
	}
	return "", "", "", false
}

// tailString returns the last n characters of s, for surfacing raw wp-cli
// output when the table parser comes up empty - a shape change upstream
// should look like unparsed text, not a silent zero.
func tailString(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// isHostLike reports whether s looks like a bare host or host:port rather
// than free text, so SearchReplace knows whether a wp-config domain rewrite
// makes sense. Names with no dot are rejected as ambiguous (a plain word
// like "staging" is far more likely to be a search term than a hostname) -
// "localhost" is the one dotless exception, since it is the single dotless
// name agent-local sites actually resolve against.
func isHostLike(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\n") {
		return false
	}
	host := hostFromURL(s)
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		if _, err := strconv.Atoi(h[i+1:]); err == nil {
			h = h[:i]
		}
	}
	if h == "" {
		return false
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	return strings.Contains(h, ".")
}

// minSearchNeedleLen guards against a "search" for "a" or "" walking every
// cell in every table - the dry-run cost is the same as a real run, so a
// needle this short is almost certainly a mistake rather than intent.
const minSearchNeedleLen = 3

// searchReplaceArgs builds the wp-cli invocation shared by DBSearch and
// SearchReplace. dryRun controls whether --dry-run is passed; the rest of
// the flags are identical either way so the two calls report on exactly the
// same scope.
func searchReplaceArgs(old, new string, dryRun bool) []string {
	args := []string{"search-replace", old, new}
	if dryRun {
		args = append(args, "--dry-run")
	}
	args = append(args,
		"--all-tables",
		"--skip-columns=guid",
		"--report-changed-only",
		"--format=table",
		"--skip-plugins",
		"--skip-themes",
	)
	return args
}

// DBSearch counts occurrences of needle across a site's database without
// changing anything. wp-cli has no standalone "count occurrences" command, so
// this is a search-replace run with --dry-run, which reports exactly the cells
// that would match and writes nothing. The replacement has to differ from the
// needle - wp-cli refuses an identical pair outright ("Skipping operation") -
// so a marker is appended; in a dry run its value never reaches a row.
func (e *Engine) DBSearch(site *Site, needle string) (*searchReport, error) {
	if len(needle) < minSearchNeedleLen {
		return nil, apiError{Code: 400, Msg: fmt.Sprintf("needle must be at least %d characters", minSearchNeedleLen)}
	}
	out, err := wpCLI(site, searchReplaceArgs(needle, needle+"\u2060agent-local-count", true)...)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, tailString(out, 300))
	}
	hits, total := parseSearchReplaceTable(out)
	rep := &searchReport{Needle: needle, DryRun: true, Hits: hits, Total: total}
	if len(hits) == 0 {
		rep.RawTail = tailString(out, 300)
	}
	return rep, nil
}

// SearchReplace runs (or dry-runs) a database-wide search-replace. When it
// actually writes (dryRun false) and old identifies a bare host or URL,
// wp-config.php's own hardcoded domain pins are rewritten too - those are
// checked before the database on every request, so a search-replace that
// only touched rows would leave the site redirecting to the old host.
func (e *Engine) SearchReplace(site *Site, old, new string, dryRun bool) (*searchReport, error) {
	if len(old) < minSearchNeedleLen {
		return nil, apiError{Code: 400, Msg: fmt.Sprintf("old must be at least %d characters", minSearchNeedleLen)}
	}
	if old == new {
		return nil, apiError{Code: 400, Msg: "old and new must differ"}
	}
	out, err := wpCLI(site, searchReplaceArgs(old, new, dryRun)...)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, tailString(out, 300))
	}
	hits, total := parseSearchReplaceTable(out)
	rep := &searchReport{Needle: old, Replacement: new, DryRun: dryRun, Hits: hits, Total: total}
	if len(hits) == 0 {
		rep.RawTail = tailString(out, 300)
	}

	if !dryRun && isHostLike(old) {
		cfgPath, cerr := wpConfigPath(site)
		if cerr == nil {
			if rerr := rewriteWPConfigDomains(cfgPath, map[string]bool{hostFromURL(old): true}, hostFromURL(new)); rerr == nil {
				rep.ConfigPinsRewritten = true
			}
		}
	}
	return rep, nil
}

// apiError carries an HTTP status code alongside a message, so engine
// functions that need to answer 400 (bad input) instead of 500/502
// (something failed) don't have to duplicate that logic in every handler.
type apiError struct {
	Code int
	Msg  string
}

func (e apiError) Error() string { return e.Msg }

// handleDBSearch answers POST /sites/{slug}/db/search {"needle": "..."}.
func (a *APIServer) handleDBSearch(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	var req struct {
		Needle string `json:"needle"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "bad json: "+err.Error())
		return
	}
	rep, err := a.engine.DBSearch(site, req.Needle)
	if err != nil {
		if ae, ok := err.(apiError); ok {
			fail(w, ae.Code, ae.Msg)
			return
		}
		fail(w, 502, err.Error())
		return
	}
	ok(w, rep)
}

// handleSearchReplace answers POST /sites/{slug}/db/search-replace
// {"old": "...", "new": "...", "dry_run": true|false}. dry_run defaults to
// true when omitted - a request that forgets the flag should never write.
func (a *APIServer) handleSearchReplace(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	var req struct {
		Old    string `json:"old"`
		New    string `json:"new"`
		DryRun *bool  `json:"dry_run"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, 400, "bad json: "+err.Error())
		return
	}
	dryRun := true
	if req.DryRun != nil {
		dryRun = *req.DryRun
	}
	rep, err := a.engine.SearchReplace(site, req.Old, req.New, dryRun)
	if err != nil {
		if ae, ok := err.(apiError); ok {
			fail(w, ae.Code, ae.Msg)
			return
		}
		fail(w, 502, err.Error())
		return
	}
	ok(w, rep)
}
