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
}

// Start launches the process detached, writing pid + log files.
// Returns the pid. Idempotent: if already running, returns existing pid.
func (p *Proc) Start() (int, error) {
	if pid, ok := p.Pid(); ok {
		return pid, nil
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
	devnull, _ := os.Open(os.DevNull)
	cmd.Stdin = devnull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("%s: start: %w", p.Name, err)
	}
	go cmd.Wait()
	if p.PidTo != "" {
		if err := os.WriteFile(p.PidTo, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
			return cmd.Process.Pid, err
		}
	}
	return cmd.Process.Pid, nil
}

// Pid reads the pid file and verifies the process is alive.
func (p *Proc) Pid() (int, bool) {
	if p.PidTo == "" {
		return 0, false
	}
	b, err := os.ReadFile(p.PidTo)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, false
	}
	if !Alive(pid) {
		return 0, false
	}
	return pid, true
}

// Stop signals the process SIGTERM, then SIGKILL after grace.
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
