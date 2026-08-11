package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PHPVersions the app knows how to install via Homebrew.
var PHPVersions = []string{"7.4", "8.0", "8.1", "8.2", "8.3", "8.4"}

// inventoryTTL is how long a scan is trusted. Toolchains change when someone runs
// brew, not between two commands, so re-running `php -v` on every invocation was
// paying ~700ms to learn what the store already knew.
const inventoryTTL = 24 * time.Hour

// EnsureInventory fills the store's Inventory, reusing the persisted scan when it
// is still true. Validity is checked with stat calls (microseconds) rather than by
// executing every toolchain (90ms each): if a recorded binary is gone, or the scan
// is old, it rescans and persists the result so the next command is cheap.
func EnsureInventory(s *Store) {
	if inventoryFresh(s.Inventory()) {
		return
	}
	DiscoverInventory(s)
	s.Inventory().Refresh = time.Now()
	// Persist so the cost is paid once, not by whoever runs the next command.
	_ = s.Save()
}

// inventoryFresh reports whether the recorded scan can still be believed.
func inventoryFresh(inv *Inventory) bool {
	if len(inv.PHPs) == 0 || inv.Refresh.IsZero() || time.Since(inv.Refresh) > inventoryTTL {
		return false
	}
	// Everything it claims exists must still exist; a brew upgrade or uninstall
	// moves these paths, and serving a site with a stale php path fails later in a
	// much more confusing place.
	for _, p := range inv.PHPs {
		if p.Bin != "" && !fileExists(p.Bin) {
			return false
		}
		if p.FPM != "" && !fileExists(p.FPM) {
			return false
		}
	}
	if inv.Brew != "" && !fileExists(inv.Brew) {
		return false
	}
	if inv.MySQL.Bin != "" && !fileExists(inv.MySQL.Bin) {
		return false
	}
	return true
}

// DiscoverInventory scans for brew, php toolchains, httpd, mysql engines
// and fills the store's Inventory. Fast: no installs.
func DiscoverInventory(s *Store) {
	inv := s.Inventory()
	inv.Brew, _ = exec.LookPath("brew")
	inv.PHPs = discoverPHPs(inv.Brew)
	inv.HTTP = discoverHTTP(inv.Brew)
	if inv.MySQL.Kind == "" {
		inv.MySQL = discoverMySQLEngine()
	}
	inv.Refresh = time.Now()
}

func brewPrefix(brew string) string {
	if brew == "" {
		return ""
	}
	out, err := exec.Command(brew, "--prefix").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func discoverPHPs(brew string) []Runtime {
	var out []Runtime
	seen := map[string]bool{}

	// Homebrew kegs: <prefix>/opt/php@X.Y and <prefix>/opt/php (latest)
	prefix := brewPrefix(brew)
	if prefix != "" {
		cands := []string{filepath.Join(prefix, "opt", "php")}
		for _, v := range PHPVersions {
			cands = append(cands, filepath.Join(prefix, "opt", "php@"+v))
		}
		// Each candidate costs a "php -v" (~90ms) and they know nothing about each
		// other, so probe them at once: eight kegs took the wall time of eight.
		found := make([]*Runtime, len(cands))
		var wg sync.WaitGroup
		for i, dir := range cands {
			wg.Add(1)
			go func(i int, dir string) {
				defer wg.Done()
				found[i] = phpFromDir(dir, "homebrew")
			}(i, dir)
		}
		wg.Wait()
		// Ordered by the candidate list, so the result does not depend on which
		// goroutine finished first.
		for _, rt := range found {
			if rt != nil && !seen[rt.Version] {
				seen[rt.Version] = true
				rt.InstallCmd = fmt.Sprintf("brew install %s", phpFormula(rt.Version))
				out = append(out, *rt)
			}
		}
	}
	// PATH fallback (php on PATH, e.g. system php)
	if p, err := exec.LookPath("php"); err == nil {
		if rt := phpFromBin(p, "path"); rt != nil && !seen[rt.Version] {
			seen[rt.Version] = true
			rt.InstallCmd = fmt.Sprintf("brew install %s", phpFormula(rt.Version))
			out = append(out, *rt)
		}
	}
	sort.Slice(out, func(i, j int) bool { return verLess(out[i].Version, out[j].Version) })
	return out
}

func phpFormula(v string) string {
	if v == latestBrewPHP() {
		return "php"
	}
	return "php@" + v
}

// latestBrewPHP is the version the unversioned `php` formula tracks.
func latestBrewPHP() string { return "8.4" }

func phpFromDir(dir, source string) *Runtime {
	bin := filepath.Join(dir, "bin", "php")
	if _, err := os.Stat(bin); err != nil {
		return nil
	}
	rt := phpFromBin(bin, source)
	if rt == nil {
		return nil
	}
	fpm := filepath.Join(dir, "sbin", "php-fpm")
	if _, err := os.Stat(fpm); err == nil {
		rt.FPM = fpm
	}
	pear := filepath.Join(dir, "bin", "pear")
	if _, err := os.Stat(pear); err == nil {
		rt.Pear = pear
	}
	return rt
}

var phpVerRe = regexp.MustCompile(`PHP (\d+\.\d+)\.\d+`)

func phpFromBin(bin, source string) *Runtime {
	out, err := exec.Command(bin, "-v").Output()
	if err != nil {
		return nil
	}
	m := phpVerRe.FindStringSubmatch(string(out))
	if m == nil {
		return nil
	}
	return &Runtime{Version: m[1], Bin: bin, Source: source}
}

func verLess(a, b string) bool {
	ap := strings.Split(a, ".")
	bp := strings.Split(b, ".")
	for i := 0; i < len(ap) && i < len(bp); i++ {
		ai, _ := strconv.Atoi(ap[i])
		bi, _ := strconv.Atoi(bp[i])
		if ai != bi {
			return ai < bi
		}
	}
	return len(ap) < len(bp)
}

func discoverHTTP(brew string) HTTPRuntime {
	if h, err := exec.LookPath("httpd"); err == nil {
		out, _ := runCmdOut(h, "-v")
		if m := regexp.MustCompile(`Apache/([\d.]+)`).FindStringSubmatch(out); m != nil {
			return HTTPRuntime{Kind: "apache", Version: m[1], Bin: h}
		}
		return HTTPRuntime{Kind: "apache", Bin: h}
	}
	return HTTPRuntime{Kind: "router"}
}

// discoverMySQLEngine finds a usable mysqld. Preference: brew mariadb,
// then brew mysql. We never adopt a broken keg (dylib mismatch) — engine.go
// validates by running --version.
func discoverMySQLEngine() MySQLRuntime {
	if brew := brewPath(); brew != "" {
		prefix := brewPrefix(brew)
		for _, name := range []string{"mariadb", "mysql"} {
			dir := filepath.Join(prefix, "opt", name)
			bin := filepath.Join(dir, "bin", name)
			if name == "mariadb" {
				bin = filepath.Join(dir, "bin", "mariadbd")
			} else {
				bin = filepath.Join(dir, "bin", "mysqld")
			}
			if v, ok := mysqldVersion(bin); ok {
				kind := "mysql"
				if name == "mariadb" {
					kind = "mariadb"
				}
				return MySQLRuntime{Kind: kind, Version: v, Dir: dir, Bin: bin}
			}
		}
	}
	// PATH fallbacks
	for _, name := range []string{"mariadbd", "mysqld"} {
		if bin, err := exec.LookPath(name); err == nil {
			if v, ok := mysqldVersion(bin); ok {
				kind := "mysql"
				if name == "mariadbd" {
					kind = "mariadb"
				}
				return MySQLRuntime{Kind: kind, Version: v, Dir: filepath.Dir(filepath.Dir(bin)), Bin: bin}
			}
		}
	}
	return MySQLRuntime{}
}

func brewPath() string {
	if b, err := exec.LookPath("brew"); err == nil {
		return b
	}
	for _, p := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func mysqldVersion(bin string) (string, bool) {
	if _, err := os.Stat(bin); err != nil {
		return "", false
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return "", false // broken keg (dylib mismatch) → reject
	}
	s := string(out)
	if m := regexp.MustCompile(`Ver ([\d.]+)`).FindStringSubmatch(s); m != nil {
		return m[1], true
	}
	if m := regexp.MustCompile(`([\d.]+)`).FindStringSubmatch(s); m != nil {
		return m[1], true
	}
	return "0", true
}

// InstallPHP installs a PHP formula via brew, reporting progress through cb.
// brew may exit nonzero on harmless caveats; success = rediscovery finds it.
func InstallPHP(s *Store, version string, cb func(line string)) error {
	inv := s.Inventory()
	if inv.Brew == "" {
		return fmt.Errorf("homebrew not found; install from https://brew.sh and retry")
	}
	formula := phpFormula(version)
	cmd := exec.Command(inv.Brew, "install", formula)
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1", "HOMEBREW_NO_PATH_SHADOW_CHECK=1")
	berr := streamCmd(cmd, cb)
	s.Data.Inv.PHPs = discoverPHPs(inv.Brew)
	if s.Inventory().FindPHP(version) == nil {
		if berr != nil {
			return fmt.Errorf("brew install %s: %w", formula, berr)
		}
		return fmt.Errorf("brew finished but php %s still not found", version)
	}
	return nil
}

// InstallMySQL installs mariadb via brew (preferred engine).
func InstallMySQL(s *Store, cb func(line string)) error {
	inv := s.Inventory()
	if inv.Brew == "" {
		return fmt.Errorf("homebrew not found; install from https://brew.sh and retry")
	}
	cmd := exec.Command(inv.Brew, "install", "mariadb")
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1", "HOMEBREW_NO_PATH_SHADOW_CHECK=1")
	berr := streamCmd(cmd, cb)
	s.Data.Inv.MySQL = discoverMySQLEngine()
	if s.Inventory().MySQL.Bin == "" {
		if berr != nil {
			return fmt.Errorf("brew install mariadb: %w", berr)
		}
		return fmt.Errorf("brew finished but no mariadb/mysqld found")
	}
	return nil
}

// InstallApache installs httpd via brew.
func InstallApache(s *Store, cb func(line string)) error {
	inv := s.Inventory()
	if inv.Brew == "" {
		return fmt.Errorf("homebrew not found; install from https://brew.sh and retry")
	}
	cmd := exec.Command(inv.Brew, "install", "httpd")
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1", "HOMEBREW_NO_PATH_SHADOW_CHECK=1")
	berr := streamCmd(cmd, cb)
	s.Data.Inv.HTTP = discoverHTTP(inv.Brew)
	if s.Inventory().HTTP.Bin == "" {
		if berr != nil {
			return fmt.Errorf("brew install httpd: %w", berr)
		}
		return fmt.Errorf("brew finished but httpd not found")
	}
	return nil
}

// InstallBrew installs Homebrew itself (interactive password prompt).
func InstallBrew(cb func(line string)) error {
	cmd := exec.Command("/bin/bash", "-c", `NONINTERACTIVE=1 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"`)
	return streamCmd(cmd, cb)
}

// streamCmd runs cmd piping each output line to cb.
func streamCmd(cmd *exec.Cmd, cb func(line string)) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	buf := make([]byte, 4096)
	var leftover []byte
	for {
		n, rerr := stdout.Read(buf)
		if n > 0 {
			leftover = append(leftover, buf[:n]...)
			for {
				idx := strings.IndexByte(string(leftover), '\n')
				if idx < 0 {
					break
				}
				line := string(leftover[:idx])
				leftover = leftover[idx+1:]
				if cb != nil {
					cb(strings.TrimRight(line, "\r"))
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	if len(leftover) > 0 && cb != nil {
		cb(string(leftover))
	}
	return cmd.Wait()
}
