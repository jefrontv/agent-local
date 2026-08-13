package main

import (
	"net/http"
	"sync/atomic"
	"testing"
)

func TestJobHubStartAndWait(t *testing.T) {
	h := NewJobHub()
	var saw atomic.Bool
	j := h.Start("import", func(cb func(string, string)) (any, error) {
		cb("files", "copying")
		saw.Store(true)
		return "ok-result", nil
	})
	if j.ID == "" {
		t.Fatal("job has no id")
	}
	j.Wait()
	if !saw.Load() {
		t.Fatal("fn never ran")
	}
	snap := j.Snapshot()
	if snap.Status != JobOK {
		t.Fatalf("status = %s error = %q", snap.Status, snap.Error)
	}
	if snap.Result != "ok-result" {
		t.Errorf("result = %v", snap.Result)
	}
	if len(snap.Steps) != 1 || snap.Steps[0].Stage != "files" {
		t.Errorf("steps = %+v", snap.Steps)
	}
	if got := h.Get(j.ID); got == nil {
		t.Fatal("Get missed the job")
	}
	list := h.List()
	if len(list) != 1 || list[0].ID != j.ID {
		t.Errorf("list = %+v", list)
	}
}

func TestJobHubRecordsFailure(t *testing.T) {
	h := NewJobHub()
	j := h.Start("create", func(func(string, string)) (any, error) {
		return nil, errJobFail
	})
	j.Wait()
	snap := j.Snapshot()
	if snap.Status != JobErr || snap.Error != errJobFail.Error() {
		t.Fatalf("snap = %+v", snap)
	}
}

func TestJobHubRetainsARing(t *testing.T) {
	h := NewJobHub()
	var last string
	for i := 0; i < jobRetain+5; i++ {
		j := h.Start("x", func(func(string, string)) (any, error) { return i, nil })
		j.Wait()
		last = j.ID
	}
	if len(h.List()) != jobRetain {
		t.Errorf("retained %d, want %d", len(h.List()), jobRetain)
	}
	if h.Get(last) == nil {
		t.Error("newest job was evicted")
	}
}

func TestWantAsync(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "/import?async=1", nil)
	if !wantAsync(req) {
		t.Error("?async=1 should be async")
	}
	req, _ = http.NewRequest(http.MethodPost, "/import", nil)
	req.Header.Set("Prefer", "respond-async")
	if !wantAsync(req) {
		t.Error("Prefer: respond-async should be async")
	}
	req, _ = http.NewRequest(http.MethodPost, "/import", nil)
	if wantAsync(req) {
		t.Error("plain POST should stay sync")
	}
}

var errJobFail = errString("nope")

type errString string

func (e errString) Error() string { return string(e) }
