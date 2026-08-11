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

// autoYieldMax bounds a yield we decided on ourselves. An explicit
// `agent-local yield` runs as long as it asked for, but a rival router that
// never binds (crashed, misconfigured) must not keep our ports for good: while
// we stand aside its wildcard listener answers our domains with its own
// "site not found", which is worse than taking the ports back.
const autoYieldMax = 20 * time.Second

// startupGrace bounds how long we wait, once at startup, for another local-dev
// router to claim :80 before we take the alias.
//
// rivalStarting cannot help here: it notices a rival that is already running, and
// our own listener is what stops one from getting that far. A wildcard bind fails
// while any specific address holds the port, so agent-local winning the boot race
// leaves LocalWP's nginx unable to start at all — its sites go dark while Local
// still reports them as running. Twenty seconds of politeness costs a late bare
// URL; skipping it costs someone their whole afternoon.
const startupGrace = 20 * time.Second

// rivalImminent reports whether it is worth waiting: another local-dev app is
// open, and nothing holds :80 yet. Deliberately based on the app being alive
// rather than on its site registry — this daemon runs as root, where the user's
// Library is not where "~" points.
func rivalImminent() bool {
	if os.Getenv("AGENT_LOCAL_NO_GRACE") != "" {
		return false
	}
	return rivalDecision(localAppRunning(), dialable("127.0.0.1", 80))
}

// rivalDecision is the judgement on its own: wait only when a rival could still
// be booting, never when one already answers.
func rivalDecision(appRunning, portTaken bool) bool {
	return appRunning && !portTaken
}

// localAppRunning reports whether LocalWP is open. Its helper process is the
// reliable marker: the app itself is a bundle whose name varies by version.
func localAppRunning() bool {
	out, err := runCmdOut("pgrep", "-f", "Local.app/Contents")
	return err == nil && strings.TrimSpace(out) != ""
}

// waitForRival gives a booting rival router the wildcard bind it needs, then
// binds alongside it. Bounded: a rival that never appears cannot keep our ports.
func waitForRival() {
	if !rivalImminent() {
		return
	}
	log.Printf("front-daemon: another local router is open but nothing holds :80 — waiting up to %s so it can bind first", startupGrace)
	deadline := time.Now().Add(startupGrace)
	for time.Now().Before(deadline) {
		if dialable("127.0.0.1", 80) {
			log.Printf("front-daemon: it bound first — taking %s:80/443 alongside it", LoopbackAlias)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	log.Printf("front-daemon: nothing claimed :80 within %s — taking %s:80/443", startupGrace, LoopbackAlias)
}

// ensureAlias puts LoopbackAlias back on lo0. macOS does not persist ifconfig
// aliases across a reboot, so after every restart the address this daemon exists
// to serve was simply gone — it had nothing to bind, and every URL fell back to
// :1080. We run as root here, so fixing it needs no prompt and no user action.
func ensureAliasBound() bool {
	ok := true
	if !interfaceHasAddr(LoopbackAlias) {
		if out, err := runCmdOut("/sbin/ifconfig", "lo0", "alias", LoopbackAlias); err != nil {
			log.Printf("front-daemon: could not add the %s alias: %v %s", LoopbackAlias, err, strings.TrimSpace(out))
			ok = false
		} else {
			log.Printf("front-daemon: re-added the %s alias to lo0 (it does not survive a reboot)", LoopbackAlias)
		}
	}
	// IPv6 is what keeps ".local" domains off the five-second mDNS path. Its
	// absence is not fatal, so it never blocks the IPv4 half.
	if !interfaceHasAddr(LoopbackAlias6) {
		if out, err := runCmdOut("/sbin/ifconfig", "lo0", "inet6", LoopbackAlias6, "prefixlen", "128", "alias"); err != nil {
			log.Printf("front-daemon: could not add the %s alias (.local will be slow): %v %s", LoopbackAlias6, err, strings.TrimSpace(out))
		} else {
			log.Printf("front-daemon: re-added the %s alias to lo0", LoopbackAlias6)
		}
	}
	return ok
}

// dialableAlias reports whether lo0 carries both halves we serve.
func dialableAlias() bool {
	return interfaceHasAddr(LoopbackAlias) && interfaceHasAddr(LoopbackAlias6)
}

// supervise keeps the listeners in the right state forever: bound normally,
// released while a competing router is starting up.
func (f *frontDaemon) supervise() {
	ensureAliasBound()
	waitForRival()
	f.bind()
	var pausedAt time.Time
	for {
		time.Sleep(1500 * time.Millisecond)
		switch {
		case f.paused && yieldActive():
			// explicitly requested: wait it out, however long it asked for
		case !dialableAlias():
			// The address we exist to serve is gone: a reboot clears lo0 aliases,
			// and removing one does not close the sockets already bound to it — so
			// we still "hold" listeners that can never accept anything. Put the
			// alias back and rebuild them.
			if ensureAliasBound() {
				f.release()
				f.bind()
			}
		case f.paused && !rivalStarting():
			f.bind()
		case f.paused && time.Since(pausedAt) > autoYieldMax:
			log.Printf("front-daemon: no rival listener after %s — taking %s:80/443 back", autoYieldMax, LoopbackAlias)
			f.bind()
		case !f.paused && rivalStarting():
			f.release()
			pausedAt = time.Now()
		case !f.paused && !f.holding():
			// Nothing bound and nobody asked us to stand aside: the first attempt
			// failed. It used to stop there — a boot with no alias yet left the
			// daemon idle forever, which is exactly how sites lost their bare URLs
			// until someone ran a command.
			ensureAliasBound()
			f.bind()
		}
	}
}

// holding reports whether we currently have the listeners.
func (f *frontDaemon) holding() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.held) > 0
}

func (f *frontDaemon) bind() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.held) > 0 {
		return
	}
	wasPaused := f.paused
	specs := []struct {
		addr string
		dst  int
	}{
		{net.JoinHostPort(LoopbackAlias, "80"), DefaultHTTPPort},
		{net.JoinHostPort(LoopbackAlias, "443"), DefaultHTTPSPort},
	}
	// The IPv6 half only exists so ".local" names resolve from /etc/hosts instead
	// of waiting on mDNS; serve it wherever it is present.
	if interfaceHasAddr(LoopbackAlias6) {
		specs = append(specs,
			struct {
				addr string
				dst  int
			}{net.JoinHostPort(LoopbackAlias6, "80"), DefaultHTTPPort},
			struct {
				addr string
				dst  int
			}{net.JoinHostPort(LoopbackAlias6, "443"), DefaultHTTPSPort})
	}
	for _, spec := range specs {
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
