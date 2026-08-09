package main

import (
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RunFrontDaemon is the root process installed as a LaunchDaemon by
// `agent-local alias`. It binds 127.0.0.2:80/:443 (needs root: ports <1024)
// and pipes each connection to the daemon's router ports. Raw TCP — HTTP and
// TLS both pass through untouched.
//
// It yields those ports while another local-dev app is trying to claim them.
// The kernel is happy to have our specific-address listener alongside a
// wildcard one, but apps like LocalWP pre-flight port 80 and refuse to start
// when anything at all is listening. So when such an app is running and its
// own listener is not up yet, we let go, wait for it to bind, then re-bind:
func RunFrontDaemon(args []string) error {
	// Runs as root, so P() would resolve to root's home. The installing user's
	// run dir arrives via the LaunchDaemon args instead.
	if v := flagValue(args, "--run-dir"); v != "" {
		frontRunDir = v
	}
	log.Printf("front-daemon: %s:80 -> :%d, %s:443 -> :%d (yield file %s)",
		LoopbackAlias, DefaultHTTPPort, LoopbackAlias, DefaultHTTPSPort, frontYieldPath())
	(&frontDaemon{}).supervise()
	return nil
}

// frontRunDir is where the daemon looks for the yield deadline file.
var frontRunDir string

type frontDaemon struct {
	mu     sync.Mutex
	held   []net.Listener
	paused bool
}

// supervise keeps the listeners in the right state forever: bound normally,
// released while a competing router is starting up.
func (f *frontDaemon) supervise() {
	f.bind()
	for {
		time.Sleep(1500 * time.Millisecond)
		switch {
		case f.paused && !rivalStarting():
			f.bind()
		case !f.paused && rivalStarting():
			f.release()
		}
	}
}

func (f *frontDaemon) bind() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.held) > 0 {
		return
	}
	wasPaused := f.paused
	for _, spec := range []struct {
		addr string
		dst  int
	}{
		{net.JoinHostPort(LoopbackAlias, "80"), DefaultHTTPPort},
		{net.JoinHostPort(LoopbackAlias, "443"), DefaultHTTPSPort},
	} {
		l, err := net.Listen("tcp", spec.addr)
		if err != nil {
			log.Printf("front-daemon: bind %s: %v (will retry)", spec.addr, err)
			continue
		}
		f.held = append(f.held, l)
		go acceptLoop(l, spec.dst)
	}
	if len(f.held) > 0 {
		f.paused = false
		if wasPaused {
			log.Printf("front-daemon: re-bound %s:80/443", LoopbackAlias)
		}
	}
}

func (f *frontDaemon) release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.held {
		l.Close()
	}
	f.held = nil
	f.paused = true
	log.Printf("front-daemon: released %s:80/443 — standing aside for another local-dev router", LoopbackAlias)
}

// rivalStarting reports whether we should stand aside. Two triggers:
//
//   - an explicit `agent-local yield` (a deadline file), which is the reliable
//     way to let another app win the ports it pre-checks;
//   - a competing *router* process (LocalWP's nginx) that is running while
//     nothing answers on 127.0.0.1:80, i.e. it is mid-startup or retrying.
//
// Per-site LocalWP services are deliberately ignored: they keep running when
// its router is down, and treating them as a signal would yield forever.
func rivalStarting() bool {
	if yieldActive() {
		return true
	}
	out, err := runCmdOut("pgrep", "-f", "lightning-services/nginx")
	if err != nil || strings.TrimSpace(out) == "" {
		return false
	}
	return !dialable("127.0.0.1", 80)
}

// yieldActive reports whether an unexpired yield request is on disk. The file
// holds a unix deadline; `agent-local yield` writes it without needing root,
// which is why this is a file and not a signal.
func yieldActive() bool {
	b, err := os.ReadFile(frontYieldPath())
	if err != nil {
		return false
	}
	deadline, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Unix() < deadline
}

// frontYieldPath is the deadline file the CLI writes and the daemon reads.
// The daemon runs as root, so the user's run dir is passed in at launch.
func frontYieldPath() string {
	dir := frontRunDir
	if dir == "" {
		dir = P().Run()
	}
	return filepath.Join(dir, "front-yield")
}

// dialable reports whether a TCP port accepts connections at an address.
func dialable(host string, port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 250*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func acceptLoop(l net.Listener, dstPort int) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return // listener closed by release(), or fatal
		}
		go pipeConn(conn, dstPort)
	}
}

func pipeConn(client net.Conn, dstPort int) {
	defer client.Close()
	backend, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(dstPort)))
	if err != nil {
		return
	}
	defer backend.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(backend, client); done <- struct{}{} }()
	go func() { io.Copy(client, backend); done <- struct{}{} }()
	<-done
}
