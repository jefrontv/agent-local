package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LocalWP is driven through the GraphQL API its app exposes on loopback. Importing
// from a halted site otherwise failed with nothing but a MySQL "can't connect",
// leaving the user to guess that the fix was to go and press Start.

// localWPConnFile holds the port and bearer token of the running app. LocalWP
// rewrites it on most launches, so the port in it can be stale.
func localWPConnFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "Local", "graphql-connection-info.json")
}

type localWPConn struct {
	Port  int    `json:"port"`
	Token string `json:"authToken"`
}

// localWPControl reads the app's connection info and finds a port that answers:
// the advertised one first, then whatever the live process is listening on, then
// the historical default. A stale port in the file is the common case.
func localWPControl() (*localWPConn, error) {
	f := localWPConnFile()
	if f == "" {
		return nil, fmt.Errorf("no home directory")
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return nil, fmt.Errorf("LocalWP is not installed or has never run (%s)", shortHome(f))
	}
	var c localWPConn
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("unreadable LocalWP connection info: %w", err)
	}
	if c.Token == "" {
		return nil, fmt.Errorf("LocalWP connection info has no auth token")
	}
	for _, port := range localWPPortCandidates(c.Port) {
		if addrOpen("127.0.0.1", port) {
			c.Port = port
			return &c, nil
		}
	}
	return nil, fmt.Errorf("LocalWP is not running — open Local and try again")
}

// localWPPortCandidates orders the ports worth trying, without duplicates.
func localWPPortCandidates(advertised int) []int {
	var out []int
	seen := map[int]bool{}
	add := func(p int) {
		if p > 0 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	add(advertised)
	for _, p := range localWPListenPorts() {
		add(p)
	}
	add(4000) // the port Local used before it started randomising
	return out
}

// localWPListenPorts asks the running app what it is bound to, since the file can
// disagree with reality.
func localWPListenPorts() []int {
	out, err := runCmdOut("lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-a", "-c", "Local")
	if err != nil {
		return nil
	}
	var ports []int
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.Contains(line, "(LISTEN)") {
			continue
		}
		// The address:port token sits just before "(LISTEN)".
		for i, f := range fields {
			if f == "(LISTEN)" && i > 0 {
				addr := fields[i-1]
				if idx := strings.LastIndex(addr, ":"); idx >= 0 {
					if p, err := strconv.Atoi(addr[idx+1:]); err == nil {
						ports = append(ports, p)
					}
				}
			}
		}
	}
	return ports
}

// localWPMutate runs one mutation against the app. Errors come back as GraphQL
// error messages, which are more useful than the HTTP status.
func localWPMutate(c *localWPConn, query string, vars map[string]interface{}) error {
	body, err := json.Marshal(map[string]interface{}{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/graphql", c.Port), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		Errors []struct{ Message string } `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("LocalWP API returned %s", resp.Status)
	}
	if len(out.Errors) > 0 {
		return fmt.Errorf("LocalWP API: %s", out.Errors[0].Message)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("LocalWP API returned %s", resp.Status)
	}
	return nil
}

// StartLocalWPSite asks Local to start one of its sites by id.
func StartLocalWPSite(id string) error {
	c, err := localWPControl()
	if err != nil {
		return err
	}
	return localWPMutate(c, "mutation ($id: ID!) { startSite(id: $id) { id } }",
		map[string]interface{}{"id": id})
}

// sourceDBReachable reports whether the source database is accepting connections,
// by the same route the dump will use: its socket when it has one, else TCP.
func sourceDBReachable(socket, host string, port int) bool {
	if socket != "" {
		if _, err := os.Stat(socket); err != nil {
			return false
		}
		c, err := net.DialTimeout("unix", socket, time.Second)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}
	if host == "" || port == 0 {
		return false
	}
	return addrOpen(host, port)
}

// ensureLocalWPRunning brings a halted LocalWP site up so its database can be
// read. It is a no-op when the site already answers, and it never leaves the
// caller guessing: every failure says what to do by hand.
// It returns the socket to dump through, which only exists once the site is up.
func ensureLocalWPRunning(id, name, socket, host string, port int, cb func(stage, detail string)) (string, error) {
	if sourceDBReachable(socket, host, port) {
		return socket, nil
	}
	cb("source", name+" is not running — asking LocalWP to start it")
	if err := StartLocalWPSite(id); err != nil {
		return socket, fmt.Errorf("%s is not running and could not be started: %w — start it in Local, then run this again", name, err)
	}
	// Local returns as soon as it has accepted the request; mysqld takes a moment
	// longer, and the dump must not race it.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		// The socket appears with the server, so look again every time rather
		// than trusting what was there while the site was halted.
		if s := localWPSocketFor(id); s != "" {
			socket = s
		}
		if sourceDBReachable(socket, host, port) {
			cb("source", name+" is up")
			return socket, nil
		}
		time.Sleep(time.Second)
	}
	return socket, fmt.Errorf("%s was started but its database did not come up within 90s — check it in Local, then run this again", name)
}
