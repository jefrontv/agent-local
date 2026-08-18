package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// keyOf names the special keys the list moves with.
func keyOf(name string) tea.KeyMsg {
	switch name {
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "pgup":
		return tea.KeyMsg{Type: tea.KeyPgUp}
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		return tea.KeyMsg{Type: tea.KeyEnd}
	}
	panic("unknown key " + name)
}

// scrollModel is a Sites tab holding n sites in a window of the given height.
func scrollModel(t *testing.T, n, height int) model {
	t.Helper()
	m := testModel(t, modeBrowse)
	for i := 0; i < n; i++ {
		s := &Site{Slug: "site-" + string(rune('a'+i%26)) + string(rune('0'+i/26)), PHPVersion: "8.4"}
		s.Domain = s.Slug + ".local"
		m.sites = append(m.sites, s)
	}
	m.width, m.height = 100, height
	return m
}

func countRows(view string) int {
	n := 0
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, ".local") {
			n++
		}
	}
	return n
}

// The Sites table used to draw every row, so a long list pushed the panel and
// the footer off a short terminal. The frame must now fit the window.
func TestSitesViewFitsShortTerminal(t *testing.T) {
	for _, h := range []int{14, 20, 30} {
		m := scrollModel(t, 40, h)
		got := strings.Count(m.View(), "\n") + 1
		if got > h {
			t.Errorf("height %d: view is %d lines", h, got)
		}
		if rows := countRows(m.View()); rows == 0 || rows >= 40 {
			t.Errorf("height %d: %d rows drawn", h, rows)
		}
	}
}

// The window follows the cursor: moving past the bottom edge scrolls by one,
// and the selected site is always on screen.
func TestSitesWindowFollowsCursor(t *testing.T) {
	m := scrollModel(t, 40, 24)
	m.layoutSites() // sizing happens in Update, not while rendering
	rows := m.pageRows
	if rows <= 0 || rows >= 40 {
		t.Fatalf("pageRows = %d", rows)
	}
	m.siteCur = rows // one past the last visible row
	m.layoutSites()
	view := m.View()
	if m.siteTop != 1 {
		t.Errorf("siteTop = %d, want 1", m.siteTop)
	}
	if want := m.sites[m.siteCur].Domain; !strings.Contains(view, want) {
		t.Errorf("selected site %q not on screen", want)
	}
	if !strings.Contains(view, "of 40") {
		t.Error("no position line while scrolled")
	}

	m.siteCur = len(m.sites) - 1
	m.layoutSites()
	view = m.View()
	if want := m.sites[m.siteCur].Domain; !strings.Contains(view, want) {
		t.Errorf("last site %q not on screen", want)
	}
	if m.siteTop != len(m.sites)-m.pageRows {
		t.Errorf("siteTop = %d, want %d", m.siteTop, len(m.sites)-m.pageRows)
	}

	m.siteCur = 0
	m.layoutSites()
	if m.siteTop != 0 {
		t.Errorf("siteTop = %d after returning to the top", m.siteTop)
	}
}

// Paging moves by a screenful and stops at the ends of the list.
func TestPageKeysMoveByScreenful(t *testing.T) {
	m := scrollModel(t, 40, 24)
	m.layoutSites()
	step := m.pageStep()
	next, _ := m.handleKey(keyOf("pgdown"))
	if got := next.(model).siteCur; got != step {
		t.Errorf("pgdown: siteCur = %d, want %d", got, step)
	}
	m.siteCur = 39
	next, _ = m.handleKey(keyOf("pgdown"))
	if got := next.(model).siteCur; got != 39 {
		t.Errorf("pgdown at the end: siteCur = %d", got)
	}
	next, _ = m.handleKey(keyOf("home"))
	if got := next.(model).siteCur; got != 0 {
		t.Errorf("home: siteCur = %d", got)
	}
	m.siteCur = 0
	next, _ = m.handleKey(keyOf("end"))
	if got := next.(model).siteCur; got != 39 {
		t.Errorf("end: siteCur = %d", got)
	}
}
