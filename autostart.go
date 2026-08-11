package main

import (
	"fmt"
	"os"
	"path/filepath"
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
		// survived but the job was unloaded.
		loadDaemonAgent(path)
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

// loadDaemonAgent (re)loads the agent for this GUI session. Best-effort: a
// failure here costs autostart, not the daemon that is already running.
func loadDaemonAgent(path string) {
	target := fmt.Sprintf("gui/%d", os.Getuid())
	// bootout first so an edited plist is picked up; ignore its error, the job
	// may simply not be loaded yet.
	runCmdQuiet("launchctl", "bootout", target+"/"+daemonAgentLabel)
	if err := runCmdQuiet("launchctl", "bootstrap", target, path); err != nil {
		// Older macOS syntax, and the fallback when bootstrap refuses because the
		// job is already loaded.
		runCmdQuiet("launchctl", "load", "-w", path)
	}
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
// autostart does not depend on a checkout staying put.
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
