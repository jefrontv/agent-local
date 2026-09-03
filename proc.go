package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Proc manages one long-lived child with a log file and pid file.
type Proc struct {
	Name   string
	Dir    string
	Args   []string
	Env    []string
	LogTo  string
	PidTo  string
	Silent bool // don't inherit stdio
	// Marker is a substring that must appear in a live pid's command line
	// (checked via `pgrep -f`, same technique as reapStrayFPM) before that pid
	// is trusted. A pid file surviving a reboot can point at a reused pid that
	// is now something else entirely; an empty Marker keeps the old
	// liveness-only check for callers that have no unique substring to check.
	Marker string
}

// Start launches the process detached, writing pid + log files.
// Returns the pid. Idempotent: if already running, returns existing pid.
func (p *Proc) Start() (int, error) {
	if pid, ok := p.Pid(); ok {
		return pid, nil
	}
	if len(p.Args) == 0 {
		return 0, fmt.Errorf("%s: no command specified", p.Name)
	}
	bin, err := exec.LookPath(p.Args[0])
	if err != nil {
		return 0, fmt.Errorf("%s: binary %q not found", p.Name, p.Args[0])
	}
	if p.LogTo != "" {
		if err := os.MkdirAll(filepath.Dir(p.LogTo), 0o755); err != nil {
			return 0, err
		}
	}
	logf, err := os.OpenFile(p.LogTo, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, fmt.Errorf("%s: open log: %w", p.Name, err)
	}
	defer logf.Close()

	cmd := exec.Command(bin, p.Args[1:]...)
	cmd.Dir = p.Dir
	cmd.Env = append(os.Environ(), p.Env...)
	cmd.Stdout = logf
	cmd.Stderr = logf
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return 0, fmt.Errorf("%s: open %s: %w", p.Name, os.DevNull, err)
	}
	cmd.Stdin = devnull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		devnull.Close()
		return 0, fmt.Errorf("%s: start: %w", p.Name, err)
	}
	// exec.Cmd duped this fd into the child; our copy is no longer needed.
	devnull.Close()
	go cmd.Wait()
	if p.PidTo != "" {
		if err := os.WriteFile(p.PidTo, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
			return cmd.Process.Pid, err
		}
	}
	return cmd.Process.Pid, nil
}

// Pid reads the pid file and verifies the process is alive — and, when
// Marker is set, that it is still the process we started rather than an
// unrelated one that reused the same pid.
func (p *Proc) Pid() (int, bool) {
	if p.PidTo == "" {
		return 0, false
	}
	b, err := os.ReadFile(p.PidTo)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if !Alive(pid) {
		return 0, false
	}
	if p.Marker != "" && !verifyPid(pid, p.Marker) {
		return 0, false
	}
	return pid, true
}

// verifyPid reports whether a live pid's command line still contains marker,
// using the same `pgrep -f` technique as engine.go's reapStrayFPM.
func verifyPid(pid int, marker string) bool {
	if pid <= 0 || marker == "" {
		return false
	}
	out, err := runCmdOut("pgrep", "-f", marker)
	if err != nil {
		return false
	}
	for _, f := range strings.Fields(out) {
		if p, err := strconv.Atoi(f); err == nil && p == pid {
			return true
		}
	}
	return false
}

// Stop signals the process SIGTERM, then SIGKILL after grace. A pid this
// process cannot verify (Marker set, no longer matching) is never
// group-killed — Pid() already refuses to trust it, so there is nothing left
// to signal, only a stale pid file to clean up.
func (p *Proc) Stop() error {
	pid, ok := p.Pid()
	if !ok {
		os.Remove(p.PidTo)
		return nil
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !Alive(pid) {
			os.Remove(p.PidTo)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
	os.Remove(p.PidTo)
	return nil
}

// Alive reports whether a pid is a live process.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks existence without delivering.
	return proc.Signal(syscall.Signal(0)) == nil
}

// PortBusy reports whether a TCP port has a listener.
func PortBusy(port int) bool {
	return portOpen(port)
}
