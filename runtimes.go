package main

import (
	"encoding/json"
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
var PHPVersions = []string{"7.4", "8.0", "8.1", "8.2", "8.3", "8.4", "8.5"}

// phpTap carries the PHP releases homebrew-core has dropped. Core deletes a
// formula once the release is end of life — 7.4 and 8.0 are already gone, and
// every version follows eventually — so an install of an old runtime has
// nowhere else to come from.
const phpTap = "shivammathur/php"

var phpVerLoose = regexp.MustCompile(`(\d+)\.(\d+)`)

var dyldPrefix = regexp.MustCompile(`^dyld\[\d+\]:\s*`)

// NormalizePHPVersion takes whatever a caller called the version and returns
// major.minor. Agents pass "php@8.2", "php8.2" and "8.2.28" as readily as
// "8.2", and every one of those used to miss the inventory and come back as
// "not installed".
func NormalizePHPVersion(v string) string {
	m := phpVerLoose.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return strings.TrimSpace(v)
	}
	return m[1] + "." + m[2]
}

// majorMinor cuts a full version ("8.5.9") down to the series ("8.5").
func majorMinor(v string) string { return NormalizePHPVersion(v) }

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
	inv.PHPs, inv.BrokenPHPs = discoverPHPs(inv.Brew)
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

// discoverPHPs scans for toolchains, returning the ones that run and the kegs
// that are installed but cannot. A keg only fails to run for a reason worth
// reporting, so the second list is how the failure reaches the caller.
func discoverPHPs(brew string) (out []Runtime, broken []Runtime) {
	seen := map[string]bool{}
	brokeSeen := map[string]bool{}

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
			if rt == nil {
				continue
			}
			if rt.Broken != "" {
				if !brokeSeen[rt.Version] {
					brokeSeen[rt.Version] = true
					rt.InstallCmd = phpRepairCmd(rt.Version)
					broken = append(broken, *rt)
				}
				continue
			}
			if !seen[rt.Version] {
				seen[rt.Version] = true
				rt.InstallCmd = phpInstallCmd(rt.Version)
				out = append(out, *rt)
			}
		}
	}
	// PATH fallback (php on PATH, e.g. system php)
	if p, err := exec.LookPath("php"); err == nil {
		if rt := phpFromBin(p, "path"); rt != nil && rt.Broken == "" && !seen[rt.Version] {
			seen[rt.Version] = true
			rt.InstallCmd = phpInstallCmd(rt.Version)
			out = append(out, *rt)
		}
	}
	sort.Slice(out, func(i, j int) bool { return verLess(out[i].Version, out[j].Version) })
	sort.Slice(broken, func(i, j int) bool { return verLess(broken[i].Version, broken[j].Version) })
	return out, broken
}

// phpInstallCmd and phpRepairCmd name our own command rather than a brew line:
// which formula, which tap and whether it needs trusting is exactly the part a
// caller cannot work out, and is what this app is for.
func phpInstallCmd(v string) string { return AppName + " install php " + v }
func phpRepairCmd(v string) string  { return AppName + " install php " + v + "   (repairs the keg)" }

// brewFormulaVersion is the stable version brew would install for a formula, or
// "" when it cannot load it at all — the formula is gone from core, or it lives
// in a tap Homebrew 6 refuses to load until it is trusted.
func brewFormulaVersion(brew, name string) string {
	if brew == "" {
		return ""
	}
	cmd := exec.Command(brew, "info", "--json=v2", "--formula", name)
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	var payload struct {
		Formulae []struct {
			Versions struct {
				Stable string `json:"stable"`
			} `json:"versions"`
		} `json:"formulae"`
	}
	if json.Unmarshal(out, &payload) != nil || len(payload.Formulae) == 0 {
		return ""
	}
	return payload.Formulae[0].Versions.Stable
}

// latestBrewPHP is the series the unversioned `php` formula currently tracks.
// This used to be the constant "8.4"; core moved php to 8.5 and the constant
// then sent `install php 8.4` to `brew install php`, which installed 8.5 and
// then reported 8.4 missing.
func latestBrewPHP() string {
	if v := majorMinor(brewFormulaVersion(brewPath(), "php")); v != "" {
		return v
	}
	return "8.5"
}

func phpFromDir(dir, source string) *Runtime {
	bin := filepath.Join(dir, "bin", "php")
	if _, err := os.Stat(bin); err != nil {
		return nil
	}
	rt := phpFromBin(bin, source)
	if rt == nil {
		return nil
	}
	if rt.Version == "" {
		// The binary would not run, so its own -v could not name the version.
		// The keg directory can: opt/php@7.4, or the unversioned latest.
		rt.Version = kegVersion(dir)
	}
	if rt.Broken != "" {
		return rt
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
	out, err := exec.Command(bin, "-v").CombinedOutput()
	if err != nil {
		// A keg whose dependency was removed exits like this, and dropping it
		// silently is what made "php 7.4 not installed" a lie. Keep it, with the
		// dyld line that says what is actually missing.
		return &Runtime{Bin: bin, Source: source, Broken: brokenReason(out, err)}
	}
	m := phpVerRe.FindStringSubmatch(string(out))
	if m == nil {
		return &Runtime{Bin: bin, Source: source, Broken: "php -v printed no version"}
	}
	return &Runtime{Version: m[1], Bin: bin, Source: source}
}

// brokenReason is the one line worth repeating out of a failed exec: the dyld
// "Library not loaded" if there is one, else the first line of output.
func brokenReason(out []byte, err error) string {
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "Library not loaded") || strings.Contains(line, "image not found") {
			// Drop dyld's own "dyld[12345]: " prefix; the pid of a dead probe is
			// noise in an error a person has to read.
			return dyldPrefix.ReplaceAllString(line, "")
		}
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return err.Error()
}

// kegVersion reads the series out of a keg path: opt/php@7.4 → 7.4, and the
// unversioned opt/php → whatever core's php formula tracks.
func kegVersion(dir string) string {
	base := filepath.Base(dir)
	if v, ok := strings.CutPrefix(base, "php@"); ok {
		return v
	}
	if base == "php" {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			if v := majorMinor(filepath.Base(real)); v != "" {
				return v
			}
		}
		return latestBrewPHP()
	}
	return ""
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
			bin := filepath.Join(dir, "bin", "mysqld")
			if name == "mariadb" {
				bin = filepath.Join(dir, "bin", "mariadbd")
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

// brewCmd is a brew invocation with the interactive noise off: auto-update turns
// a 30-second install into a five-minute one, and the hints go to a caller that
// is a program.
func brewCmd(brew string, args ...string) *exec.Cmd {
	cmd := exec.Command(brew, args...)
	cmd.Env = append(os.Environ(),
		"HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_ENV_HINTS=1",
		"HOMEBREW_NO_PATH_SHADOW_CHECK=1", "HOMEBREW_NO_INSTALL_CLEANUP=1")
	return cmd
}

// rescanPHP re-reads the toolchains into the store after brew has changed them.
func rescanPHP(s *Store) {
	s.Data.Inv.PHPs, s.Data.Inv.BrokenPHPs = discoverPHPs(s.Inventory().Brew)
	s.Data.Inv.Refresh = time.Now()
}

// maybeRescanPHP rescans only when the recorded scan is older than maxAge. A
// lookup that misses is a reason to check the disk once, not a reason to run
// `php -v` over every keg again on the next call a second later.
func maybeRescanPHP(s *Store, maxAge time.Duration) {
	if time.Since(s.Inventory().Refresh) > maxAge {
		rescanPHP(s)
	}
}

// InstallPHP makes a PHP version usable, whatever is standing in the way:
// a keg that is missing, a keg that is installed but broken, a formula core has
// deleted. brew may exit nonzero on harmless caveats, so success is always
// "rediscovery finds a php that runs", never brew's exit code.
//
// allowTap permits the third-party phpTap for versions homebrew-core no longer
// carries. It is off by default because trusting a tap lets its formulae run
// code on this machine, which is the caller's decision to make, not ours.
func InstallPHP(s *Store, version string, allowTap bool, cb func(line string)) error {
	version = NormalizePHPVersion(version)
	if cb == nil {
		cb = func(string) {}
	}
	inv := s.Inventory()
	if inv.Brew == "" {
		return fmt.Errorf("homebrew not found; install from https://brew.sh and retry")
	}
	if inv.FindPHP(version) != nil {
		cb(fmt.Sprintf("php %s already installed", version))
		return nil
	}
	// An installed-but-broken keg is the common case and needs repair, not an
	// install: brew would just say "already installed" and change nothing.
	if inv.FindBrokenPHP(version) != nil {
		if err := RepairPHP(s, version, cb); err == nil {
			return nil
		} else if s.Inventory().FindBrokenPHP(version) != nil {
			return err
		}
	}
	formula, err := phpFormulaFor(inv.Brew, version, allowTap, cb)
	if err != nil {
		return err
	}
	cb("brew install " + formula)
	berr := streamCmd(brewCmd(inv.Brew, "install", formula), cb)
	rescanPHP(s)
	if s.Inventory().FindPHP(version) != nil {
		return nil
	}
	if rt := s.Inventory().FindBrokenPHP(version); rt != nil {
		return fmt.Errorf("php %s installed but will not run: %s; try `%s`", version, rt.Broken, phpRepairCmd(version))
	}
	if berr != nil {
		return fmt.Errorf("brew install %s: %w", formula, berr)
	}
	return fmt.Errorf("brew install %s finished but php %s is still not there (installed: %s)",
		formula, version, strings.Join(s.Inventory().Runtimes(), ", "))
}

// phpFormulaFor picks the formula that actually provides a version: the
// versioned core formula when it still exists, the unversioned php when it
// happens to track that series, and the third-party tap for anything core has
// dropped. Guessing "php@X.Y" was wrong in both directions — 7.4 and 8.0 are no
// longer in core, and 8.4 is no longer what plain `php` installs.
func phpFormulaFor(brew, version string, allowTap bool, cb func(string)) (string, error) {
	if majorMinor(brewFormulaVersion(brew, "php@"+version)) == version {
		return "php@" + version, nil
	}
	if majorMinor(brewFormulaVersion(brew, "php")) == version {
		return "php", nil
	}
	tapped := phpTap + "/php@" + version
	if !allowTap {
		return "", fmt.Errorf("php %s is not in homebrew-core any more; it is only in the third-party %s tap. "+
			"Retry with tap enabled to allow it (CLI: `%s install php %s --tap`, MCP: install_runtime {\"what\":\"php\",\"version\":\"%s\",\"tap\":true}), "+
			"or install it yourself: brew tap %s && brew trust --tap %s && brew install %s",
			version, phpTap, AppName, version, version, phpTap, phpTap, tapped)
	}
	cb("brew tap " + phpTap)
	if err := streamCmd(brewCmd(brew, "tap", phpTap), cb); err != nil {
		return "", fmt.Errorf("brew tap %s: %w", phpTap, err)
	}
	// Homebrew 6 refuses to load a formula from a third-party tap until it is
	// trusted, and says so instead of installing. Older brews have no `trust`
	// subcommand at all, so a failure here is not fatal on its own.
	cb("brew trust --tap " + phpTap)
	if err := streamCmd(brewCmd(brew, "trust", "--tap", phpTap), cb); err != nil {
		cb("brew trust failed (older brew?): " + err.Error())
	}
	return tapped, nil
}

// RepairPHP puts a broken keg back together. The usual cause is `brew
// autoremove` taking a dependency php still links against — the keg is intact,
// one dylib is gone, and reinstalling that one formula fixes it in seconds
// instead of rebuilding php.
func RepairPHP(s *Store, version string, cb func(line string)) error {
	version = NormalizePHPVersion(version)
	if cb == nil {
		cb = func(string) {}
	}
	inv := s.Inventory()
	if inv.Brew == "" {
		return fmt.Errorf("homebrew not found; install from https://brew.sh and retry")
	}
	rt := inv.FindBrokenPHP(version)
	if rt == nil {
		if inv.FindPHP(version) != nil {
			return nil
		}
		return fmt.Errorf("no php %s keg on disk to repair", version)
	}
	deps, tap := kegMissingDeps(rt.Bin)
	if len(deps) > 0 {
		cb(fmt.Sprintf("php %s is linked against %s, which is not installed", version, strings.Join(deps, ", ")))
		if err := streamCmd(brewCmd(inv.Brew, append([]string{"install"}, deps...)...), cb); err != nil {
			cb("brew install " + strings.Join(deps, " ") + ": " + err.Error())
		}
		rescanPHP(s)
		if s.Inventory().FindPHP(version) != nil {
			return nil
		}
	}
	// Dependencies were not the whole story: rebuild the keg itself.
	formula := "php@" + version
	if tap != "" && tap != "homebrew/core" {
		formula = tap + "/php@" + version
	}
	cb("brew reinstall " + formula)
	rerr := streamCmd(brewCmd(inv.Brew, "reinstall", formula), cb)
	rescanPHP(s)
	if s.Inventory().FindPHP(version) != nil {
		return nil
	}
	broken := rt.Broken
	if now := s.Inventory().FindBrokenPHP(version); now != nil {
		broken = now.Broken
	}
	msg := fmt.Sprintf("php %s is installed at %s but will not run: %s", version, rt.Bin, broken)
	if rerr != nil {
		msg += fmt.Sprintf("; brew reinstall %s also failed: %v", formula, rerr)
	}
	if tap != "" && tap != "homebrew/core" {
		msg += fmt.Sprintf("; it came from the %s tap, which Homebrew will not load until it is trusted: brew trust --tap %s", tap, tap)
	}
	return fmt.Errorf("%s", msg)
}

// kegMissingDeps reads the keg's own Homebrew receipt and reports which of its
// runtime dependencies are no longer installed, plus the tap it came from. The
// receipt is the only place that records this without asking brew to load a
// formula, which for a third-party tap it may refuse to do.
func kegMissingDeps(bin string) (missing []string, tap string) {
	keg := filepath.Dir(filepath.Dir(bin)) // <prefix>/opt/php@X.Y
	real, err := filepath.EvalSymlinks(keg)
	if err != nil {
		real = keg
	}
	raw, err := os.ReadFile(filepath.Join(real, "INSTALL_RECEIPT.json"))
	if err != nil {
		return nil, ""
	}
	var receipt struct {
		RuntimeDependencies []struct {
			FullName string `json:"full_name"`
		} `json:"runtime_dependencies"`
		Source struct {
			Tap string `json:"tap"`
		} `json:"source"`
	}
	if json.Unmarshal(raw, &receipt) != nil {
		return nil, ""
	}
	prefix := filepath.Dir(filepath.Dir(keg)) // <prefix>
	seen := map[string]bool{}
	for _, d := range receipt.RuntimeDependencies {
		name := d.FullName
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if !fileExists(filepath.Join(prefix, "opt", name)) {
			missing = append(missing, d.FullName)
		}
	}
	return missing, receipt.Source.Tap
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
