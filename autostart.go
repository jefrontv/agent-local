package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The front daemon has always had a LaunchDaemon, but the router/API daemon had
// nothing: after a reboot it only came up when someone happened to run a command,
// so sites that were running before the shutdown appeared to be off. This installs
// a per-user LaunchAgent so a login brings the whole stack back by itself.

// daemonAgentLabel identifies the LaunchAgent.
const daemonAgentLabel = "local.agent-local.daemon"

// launchdMarker is set in the plist so the process launchd starts can recognise
// itself and leave the agent alone.
const launchdMarker = "AGENT_LOCAL_LAUNCHD"

// daemonAgentPath is where the per-user agent lives. LaunchAgents need no root,
// which is why this is separate from the privileged front daemon.
func daemonAgentPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "LaunchAgents", daemonAgentLabel+".plist")
}

// EnsureDaemonAutostart writes and loads the LaunchAgent if it is missing or
// out of date. Quiet and idempotent: it runs whenever the daemon is started, so
// the guarantee arrives without anyone having to know about it.
func EnsureDaemonAutostart() error {
	// Never when we are the job: reloading the agent boots out the label, which
	// is this very process — the daemon killed itself moments after taking over.
	if os.Getenv(launchdMarker) != "" {
		return nil
	}
	path := daemonAgentPath()
	if path == "" {
		return fmt.Errorf("no home directory")
	}
	bin, err := installedBinaryPath()
	if err != nil {
		return err
	}
	want := daemonPlist(bin)
	if cur, err := os.ReadFile(path); err == nil && string(cur) == want {
		// Already correct. Make sure it is actually loaded, in case the plist
		// survived but the job was unloaded — but only then. Reloading a job
		// that is already loaded boots out its running process, and this is
		// called from every CLI and MCP invocation: it was restarting the
		// daemon on every single tool call.
		if !daemonAgentLoaded() {
			loadDaemonAgent(path)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		return err
	}
	loadDaemonAgent(path)
	return nil
}

// daemonAgentLoaded reports whether launchd has the agent in this GUI
// session. `launchctl print` exits 0 for a loaded label and non-zero (113)
// for an unknown one.
func daemonAgentLoaded() bool {
	return runCmdQuiet("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), daemonAgentLabel)) == nil
}

// loadDaemonAgent (re)loads the agent for this GUI session. Best-effort: a
// failure here costs autostart, not the daemon that is already running.
//
// bootout is asynchronous on the launchd side: it returns while the job's
// process is still exiting, and a bootstrap that lands inside that window is
// refused with EIO ("Input/output error") as if the label were still taken.
// One refused bootstrap used to send the caller down the direct-fork
// fallback, producing an unsupervised daemon nobody would restart. Retry
// across the window; it is a few hundred milliseconds at most.
func loadDaemonAgent(path string) {
	target := fmt.Sprintf("gui/%d", os.Getuid())
	// bootout first so an edited plist is picked up; ignore its error, the job
	// may simply not be loaded yet.
	runCmdQuiet("launchctl", "bootout", target+"/"+daemonAgentLabel)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if runCmdQuiet("launchctl", "bootstrap", target, path) == nil {
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	// Older macOS syntax, and the fallback when bootstrap keeps refusing.
	runCmdQuiet("launchctl", "load", "-w", path)
}

// RemoveDaemonAutostart unloads and deletes the agent.
func RemoveDaemonAutostart() {
	path := daemonAgentPath()
	if path == "" {
		return
	}
	runCmdQuiet("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), daemonAgentLabel))
	os.Remove(path)
}

// installedBinaryPath is the binary the agent should run. The running executable
// may be a build in a working tree; prefer the installed copy when it exists so
// autostart does not depend on a checkout staying put. A Homebrew binary is
// named by its `bin` symlink, not the versioned Caskroom file behind it: brew
// upgrade removes that file, and a plist pointing at it starts nothing after
// the next reboot.
func installedBinaryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err == nil {
		installed := filepath.Join(home, ".local", "bin", AppName)
		if fileExists(installed) {
			return installed, nil
		}
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if link := homebrewLink(self); link != "" {
		return link, nil
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		return resolved, nil
	}
	return self, nil
}

// daemonPlist runs the daemon in the foreground so launchd can supervise it:
// --background daemonises, which launchd reads as the job having exited.
//
// KeepAlive only on failure, because a clean exit is how a second instance steps
// aside when one is already serving — restarting that forever would be a loop.
func daemonPlist(bin string) string {
	logPath := filepath.Join(P().Logs(), "daemon-agent.log")
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string><string>daemon</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict><key>%s</key><string>1</string></dict>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key><false/>
	</dict>
	<key>ProcessType</key><string>Interactive</string>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, daemonAgentLabel, bin, launchdMarker, logPath, logPath)
}
