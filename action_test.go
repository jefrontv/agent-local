package main

import (
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// testModel is a model wired to a throwaway store, as the real one always is:
// completing an action refreshes, and refreshing reads the store.
func testModel(t *testing.T, mode mode) model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	store, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	return model{store: store, engine: NewEngine(store), mode: mode}
}

// Actions used to run inside Update, so bubbletea could not paint until they
// finished: a domain change froze the whole screen for seconds. The work now runs
// in a goroutine and reports back through messages, so the state transitions are
// what has to hold.
func TestRunActionEntersBusyAndReportsBack(t *testing.T) {
	m := testModel(t, modeBrowse)
	started := make(chan struct{})
	next, cmd := m.runAction("doing the thing", func(cb func(string, string)) (string, error) {
		close(started)
		cb("stage", "detail")
		return "all done", nil
	})
	if next.mode != modeBusy {
		t.Fatalf("mode = %v, want modeBusy", next.mode)
	}
	if next.busy != "doing the thing" {
		t.Errorf("busy label = %q", next.busy)
	}
	if next.progress == nil {
		t.Fatal("no progress channel")
	}
	if cmd == nil {
		t.Fatal("no command returned; nothing would drive the spinner or the wait")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the action never ran: it must not wait for Update to return")
	}

	// Drain the two messages the action produces, as bubbletea's command loop does.
	first := waitForAction(next.progress)().(actionMsg)
	if first.done {
		t.Fatalf("expected the progress step first, got done")
	}
	mid, _ := next.Update(first)
	busy := mid.(model)
	if busy.busyDetail != "stage detail" {
		t.Errorf("progress detail = %q, want %q", busy.busyDetail, "stage detail")
	}
	if busy.mode != modeBusy {
		t.Error("a progress step must not end the busy state")
	}

	last := waitForAction(busy.progress)().(actionMsg)
	if !last.done || last.result != "all done" {
		t.Fatalf("final message = %+v", last)
	}
}

// A failure has to surface as the error message, not vanish with the goroutine.
func TestActionFailureIsReported(t *testing.T) {
	m := testModel(t, modeBusy)
	m.busy, m.progress = "working", make(chan actionMsg, 1)
	out, _ := m.Update(actionMsg{done: true, err: errors.New("it went wrong")})
	got := out.(model)
	if got.mode != modeBrowse {
		t.Errorf("mode = %v, want back to browse", got.mode)
	}
	if got.busy != "" || got.busyDetail != "" {
		t.Errorf("busy state not cleared: %q / %q", got.busy, got.busyDetail)
	}
	if !got.msgErr || got.msg != "it went wrong" {
		t.Errorf("msg = %q (err %v), want the failure surfaced", got.msg, got.msgErr)
	}
}

// A value produced in the goroutine reaches the model through the message, since
// assigning to the model from there writes to a copy nobody renders.
func TestActionPayloadReachesModel(t *testing.T) {
	m := testModel(t, modeBusy)
	m.progress = make(chan actionMsg, 1)
	rep := &DoctorReport{Findings: []Finding{{Check: "x", Status: "ok"}}}
	out, _ := m.Update(actionMsg{done: true, result: "checks re-run", payload: rep})
	got := out.(model)
	if got.doctor != rep {
		t.Error("the doctor report produced in the background never reached the model")
	}
}

// The spinner keeps ticking while busy and stops when the action ends, so a
// finished action cannot leave a timer running forever.
func TestSpinnerTicksOnlyWhileBusy(t *testing.T) {
	busy := testModel(t, modeBusy)
	out, cmd := busy.Update(spinTickMsg{})
	if cmd == nil {
		t.Error("a busy spinner tick must schedule the next one")
	}
	if out.(model).spin != 1 {
		t.Errorf("spin = %d, want it advanced", out.(model).spin)
	}
	idle := testModel(t, modeBrowse)
	if _, cmd := idle.Update(spinTickMsg{}); cmd != nil {
		t.Error("an idle model must not keep the spinner scheduled")
	}
}

// While an action runs, keys other than ctrl+c are ignored: acting on a
// half-changed site is how one problem becomes two.
func TestKeysAreHeldWhileBusy(t *testing.T) {
	m := testModel(t, modeBusy)
	m.busy = "working"
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'D'}},
		{Type: tea.KeyRunes, Runes: []rune{'n'}},
		{Type: tea.KeyEnter},
	} {
		out, _ := m.handleKey(key)
		if got := out.(model); got.mode != modeBusy || got.quitting {
			t.Errorf("key %v changed state while busy: mode=%v quitting=%v", key, got.mode, got.quitting)
		}
	}
	out, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !out.(model).quitting || cmd == nil {
		t.Error("ctrl+c must still quit while busy")
	}
}
