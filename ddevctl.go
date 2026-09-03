package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DDEV projects are the other common "local WordPress" an import comes from.
// Everything about one is a `ddev` call away — `ddev list -j` and
// `ddev describe -j` return JSON — and the database lives in a container that
// only exists while the project runs, so a stopped project is started first,
// the way a halted LocalWP site is. Docker not running is the one failure
// LocalWP never has, and it is named as such rather than surfacing as a
// connection error.

// DDEVProject is the slice of `ddev describe -j` an import needs.
type DDEVProject struct {
	Name       string      `json:"name"`
	Status     string      `json:"status"`
	Type       string      `json:"type"`
	AppRoot    string      `json:"approot"`
	Docroot    string      `json:"docroot"` // relative to AppRoot, "" when the root is the docroot
	PHPVersion string      `json:"php_version"`
	PrimaryURL string      `json:"primary_url"`
	Hostnames  []string    `json:"hostnames"`
	DBType     string      `json:"database_type"`
	DBVersion  string      `json:"database_version"`
	DBInfo     *ddevDBInfo `json:"dbinfo"`
}

// ddevDBInfo is how to reach the database from the host, present only while
// DDEV considers the project running.
type ddevDBInfo struct {
	PublishedPort int    `json:"published_port"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	DBName        string `json:"dbname"`
}

// DocrootPath is the absolute directory WordPress lives in.
func (p DDEVProject) DocrootPath() string { return filepath.Join(p.AppRoot, p.Docroot) }

// Running reports whether the containers are up.
func (p DDEVProject) Running() bool { return p.Status == "running" }

// dbCreds returns how to reach the project's database from the host, or
// zero values while it is stopped (the port is only published when running).
func (p DDEVProject) dbCreds() (port int, user, pass, name string) {
	if p.DBInfo == nil {
		return 0, "", "", ""
	}
	user, pass, name = p.DBInfo.Username, p.DBInfo.Password, p.DBInfo.DBName
	if user == "" {
		user, pass, name = "db", "db", "db" // DDEV's fixed defaults
	}
	return p.DBInfo.PublishedPort, user, pass, name
}

// ddevBin is the CLI, or "" when DDEV is not installed. The daemon's PATH is
// launchd's, not the shell's, so Homebrew's prefixes are tried by hand.
func ddevBin() string { return toolBin("ddev") }

// dockerBin is the Docker CLI DDEV itself drives.
func dockerBin() string { return toolBin("docker") }

func toolBin(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin", filepath.Join(HomeDir(), ".rd", "bin")} {
		if p := filepath.Join(dir, name); fileExists(p) {
			return p
		}
	}
	return ""
}

// ddevJSON runs a ddev subcommand with -j and returns its "raw" payload.
// A fatal log line (Docker down, unknown project) becomes the error text.
func ddevJSON(args ...string) (json.RawMessage, error) {
	bin := ddevBin()
	if bin == "" {
		return nil, fmt.Errorf("ddev is not installed")
	}
	out, err := exec.Command(bin, append(args, "-j")...).CombinedOutput()
	var msg struct {
		Level string          `json:"level"`
		Msg   string          `json:"msg"`
		Raw   json.RawMessage `json:"raw"`
	}
	// -j output is one JSON object per line; the payload is the last one.
	var last []byte
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "{") {
			last = []byte(line)
		}
	}
	if last == nil {
		if err != nil {
			return nil, fmt.Errorf("ddev %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil, fmt.Errorf("ddev %s: no JSON in output", strings.Join(args, " "))
	}
	if err := json.Unmarshal(last, &msg); err != nil {
		return nil, fmt.Errorf("ddev %s: %w", strings.Join(args, " "), err)
	}
	if msg.Level == "fatal" || msg.Level == "error" {
		return nil, fmt.Errorf("%s", ddevPlainError(msg.Msg))
	}
	return msg.Raw, nil
}

// ddevPlainError turns DDEV's fatal messages into one line that says what to do.
func ddevPlainError(msg string) string {
	if strings.Contains(msg, "Docker provider") {
		return "Docker is not running; start Docker Desktop (or `colima start`), then retry"
	}
	return strings.SplitN(msg, "\n", 2)[0]
}

// ListDDEVProjects returns every project DDEV knows about. With Docker down,
// `ddev list` refuses, so the registry file is read instead: names and roots
// are still useful for `import`, with the status saying why nothing else is.
func ListDDEVProjects() ([]DDEVProject, error) {
	if ddevBin() == "" {
		return nil, fmt.Errorf("ddev is not installed")
	}
	raw, err := ddevJSON("list")
	if err == nil {
		var ps []DDEVProject
		if jerr := json.Unmarshal(raw, &ps); jerr != nil {
			return nil, jerr
		}
		return ps, nil
	}
	ps := ddevRegistry()
	if len(ps) == 0 {
		return nil, err
	}
	for i := range ps {
		ps[i].Status = "docker not running"
	}
	return ps, nil
}

// ddevRegistry parses ~/.ddev/project_list.yaml, a two-level map of
// name → {approot}. It is simple enough that a real YAML parser is not worth
// a dependency; anything else in it is ignored.
func ddevRegistry() []DDEVProject {
	b, err := os.ReadFile(filepath.Join(HomeDir(), ".ddev", "project_list.yaml"))
	if err != nil {
		return nil
	}
	var ps []DDEVProject
	var cur *DDEVProject
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			ps = append(ps, DDEVProject{Name: strings.TrimSuffix(strings.TrimSpace(line), ":")})
			cur = &ps[len(ps)-1]
			continue
		}
		if cur != nil {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && strings.TrimSpace(k) == "approot" {
				cur.AppRoot = strings.Trim(strings.TrimSpace(v), `"'`)
			}
		}
	}
	return ps
}

// DescribeDDEVProject is one project in full, including database access
// details when it is running.
func DescribeDDEVProject(name string) (*DDEVProject, error) {
	raw, err := ddevJSON("describe", name)
	if err != nil {
		return nil, err
	}
	var p DDEVProject
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// findDDEVProject matches an import source against DDEV's projects by name
// or by path (the approot or anything inside it). nil when it is not one.
func findDDEVProject(source string) *DDEVProject {
	if ddevBin() == "" {
		return nil
	}
	ps, err := ListDDEVProjects()
	if err != nil {
		return nil
	}
	for i := range ps {
		if ddevMatches(ps[i], source) {
			return &ps[i]
		}
	}
	return nil
}

// ddevMatches is the match rule on its own: the project's name, or an
// existing directory at or below its approot. A parent directory is not a
// match — "~/Sites" names every project under it and none in particular.
func ddevMatches(p DDEVProject, source string) bool {
	if p.Name == source {
		return true
	}
	if st, err := os.Stat(source); err != nil || !st.IsDir() {
		return false
	}
	root := normalizePath(p.AppRoot)
	want := normalizePath(source)
	return root != "" && want != "" && pathWithin(want, root)
}

// ensureDDEVRunning brings a project up so its database can be read, and
// returns it re-described with the published database port. Every failure
// says what to do by hand.
func ensureDDEVRunning(p *DDEVProject, cb func(stage, detail string)) (*DDEVProject, error) {
	if p.Status == "docker not running" {
		return nil, fmt.Errorf("%s is a DDEV project, but Docker is not running; start Docker Desktop (or `colima start`) and retry", p.Name)
	}
	if !p.Running() {
		cb("source", p.Name+" is not running — asking DDEV to start it")
		bin := ddevBin()
		cmd := exec.Command(bin, "start", "-y", "--", p.Name)
		if err := streamCmd(cmd, func(line string) {
			if line = strings.TrimSpace(line); line != "" {
				cb("ddev", line)
			}
		}); err != nil {
			return nil, fmt.Errorf("ddev start %s: %w (start it by hand with `ddev start %s`, then retry)", p.Name, err, p.Name)
		}
	}
	full, err := DescribeDDEVProject(p.Name)
	if err != nil {
		return nil, err
	}
	port, _, _, _ := full.dbCreds()
	if port == 0 {
		// DDEV fills dbinfo only when it judges the project healthy (Mutagen
		// hiccups make it "unhealthy" with the database up and fine). The
		// container's published port is the fact that matters; ask Docker.
		if port = ddevDBPortFromDocker(p.Name); port != 0 {
			full.DBInfo = &ddevDBInfo{PublishedPort: port, Username: "db", Password: "db", DBName: "db"}
		}
	}
	if port == 0 {
		return nil, fmt.Errorf("%s is running but no database port is published; run `ddev export-db --file dump.sql.gz` and import with --sql dump.sql.gz", p.Name)
	}
	cb("source", fmt.Sprintf("%s is up — database on 127.0.0.1:%d", p.Name, port))
	return full, nil
}

// ddevDBPortFromDocker reads the host port mapped to the db container's 3306,
// straight from Docker: `docker port ddev-NAME-db 3306/tcp` → 127.0.0.1:32771.
func ddevDBPortFromDocker(name string) int {
	bin := dockerBin()
	if bin == "" {
		return 0
	}
	out, err := runCmdOut(bin, "port", "ddev-"+name+"-db", "3306/tcp")
	if err != nil {
		return 0
	}
	return parseDockerPort(out)
}

// parseDockerPort takes `docker port` output — one "host:port" per line, IPv6
// hosts in brackets — and returns the first host port, 0 when there is none.
func parseDockerPort(out string) int {
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if i := strings.LastIndexByte(l, ':'); i >= 0 {
			if port := atoi0(strings.TrimSpace(l[i+1:])); port != 0 {
				return port
			}
		}
	}
	return 0
}

// detachDDEVProject moves a project out of DDEV once it lives here: the
// containers and their database go, DDEV forgets the project, the codebase
// and its .ddev/ folder stay. DDEV takes its own snapshot first (under
// .ddev/db_snapshots), so `ddev start` + `ddev snapshot restore` undoes it.
func detachDDEVProject(name string, cb func(stage, detail string)) error {
	bin := ddevBin()
	if bin == "" {
		return fmt.Errorf("ddev is not installed")
	}
	cb("ddev", "removing the DDEV project (its own snapshot is kept in .ddev/db_snapshots)")
	cmd := exec.Command(bin, "delete", "-y", "--", name)
	return streamCmd(cmd, func(line string) {
		if line = strings.TrimSpace(line); line != "" {
			cb("ddev", line)
		}
	})
}
