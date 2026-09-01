package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeReport builds a report of n findings; every fifth one warns and
// carries a fix hint, which costs the window a second line.
func fakeReport(n int) *DoctorReport {
	rep := &DoctorReport{}
	for i := range n {
		f := Finding{Check: fmt.Sprintf("site:demo-%02d", i), Status: "ok", Detail: "http 200"}
		if i%5 == 4 {
			f.Status = "warn"
			f.Detail = "http 200 but slow"
			f.FixCmd = "agent-local restart demo"
		}
		rep.Findings = append(rep.Findings, f)
	}
	return rep
}

// A report taller than the terminal used to render whole, pushing the footer
// off screen with no way to scroll. The frame must fit the window now.
func TestDoctorViewFitsShortTerminal(t *testing.T) {
	for _, h := range []int{14, 24, 40} {
		m := testModel(t, modeBrowse)
		m.width, m.height = 100, h
		m.tab = tabDoctor
		m.doctor = fakeReport(120)
		m.layout()
		view := m.View()
		if got := lineCount(view); got > h {
			t.Errorf("height %d: frame is %d lines", h, got)
		}
		if !strings.Contains(view, "of 120") {
			t.Errorf("height %d: no position line while clipped", h)
		}
	}
}

// The window follows the cursor to the end of the report and back.
func TestDoctorWindowFollowsCursor(t *testing.T) {
	m := testModel(t, modeBrowse)
	m.width, m.height = 100, 24
	m.tab = tabDoctor
	m.doctor = fakeReport(120)
	m.docCur = 119
	m.layout()
	view := m.View()
	if !strings.Contains(view, "site:demo-119") {
		t.Error("last finding not on screen with cursor on it")
	}
	m.docCur = 0
	m.layout()
	if m.docTop != 0 {
		t.Errorf("docTop = %d after returning to the top", m.docTop)
	}
}

// Rendering with no report yet must show a placeholder, never run the
// checks: probing every site inside View() froze the tab on entry — and on
// a value receiver the result was discarded, so every frame paid for it.
func TestDoctorViewWithoutReportIsInert(t *testing.T) {
	m := testModel(t, modeBrowse)
	m.width, m.height = 100, 24
	m.tab = tabDoctor
	view := m.View()
	if !strings.Contains(view, "running checks") {
		t.Errorf("placeholder missing:\n%s", view)
	}
	if m.doctor != nil {
		t.Error("View populated the report itself")
	}
}

// Entering the Doctor tab kicks the checks off asynchronously exactly once.
func TestDoctorTabEntryRunsChecksAsync(t *testing.T) {
	tabKey := tea.KeyMsg{Type: tea.KeyTab}
	rKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}
	m := testModel(t, modeBrowse)
	m.width, m.height = 100, 24
	m.tab = tabRuntimes // one tab before doctor
	next, cmd := m.handleKey(tabKey)
	nm := next.(model)
	if nm.tab != tabDoctor || cmd == nil || nm.mode != modeBusy {
		t.Fatalf("tab entry: tab=%v cmd=%v mode=%v", nm.tab, cmd != nil, nm.mode)
	}
	// With a report cached, entry is free.
	nm.mode = modeBrowse
	nm.doctor = fakeReport(3)
	nm.tab = tabRuntimes
	next, cmd = nm.handleKey(tabKey)
	if cmd != nil {
		t.Error("re-entering the tab re-ran the checks")
	}
	// "r" on the tab re-runs them (the global refresh used to swallow it).
	next, cmd = next.(model).handleKey(rKey)
	if cmd == nil || next.(model).mode != modeBusy {
		t.Error("r on the doctor tab did not re-run the checks")
	}
}

func TestDoctorWindowCosts(t *testing.T) {
	rep := fakeReport(20) // findings 4, 9, 14, 19 cost two lines
	// Budget 6: rows 0..4 cost 1+1+1+1+2 = 6.
	top, end := doctorWindow(rep.Findings, 0, 0, 6)
	if top != 0 || end != 5 {
		t.Errorf("window = %d..%d, want 0..5", top, end)
	}
	// Cursor below the window slides it down.
	top, end = doctorWindow(rep.Findings, 10, 0, 6)
	if top == 0 || end <= 10 {
		t.Errorf("cursor 10 not brought into %d..%d", top, end)
	}
	// A stale top scrolled past a shrunken report is pulled back up.
	top, end = doctorWindow(rep.Findings[:8], 7, 7, 40)
	if top != 0 || end != 8 {
		t.Errorf("stale top left blank budget: %d..%d", top, end)
	}
	// Degenerate: budget too small for a two-line row still terminates.
	top, end = doctorWindow(rep.Findings, 4, 0, 1)
	if top != 4 || end != 4 {
		t.Errorf("tight budget = %d..%d", top, end)
	}
	if top, end = doctorWindow(nil, 0, 0, 10); top != 0 || end != 0 {
		t.Errorf("empty report = %d..%d", top, end)
	}
}
