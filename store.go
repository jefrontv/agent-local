package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Store is the persisted catalog of sites + worktrees + inventory.
// The daemon calls ReloadIfChanged before reads so CLI writes are visible;
// all disk IO goes through an advisory flock so the two never interleave.
type Store struct {
	mu       sync.Mutex
	path     string
	loadedAt time.Time
	// base is the document as last loaded or written: the common ancestor a save
	// diffs against, so only this process's own changes are pushed to disk.
	base []byte
	// deleted records removals this process asked for, per collection. Absence
	// from memory is never enough to delete another process's record.
	deleted map[string]map[string]bool
	Data    storeData
}

type storeData struct {
	Sites     map[string]*Site     `json:"sites"`
	Worktrees map[string]*Worktree `json:"worktrees"`
	Front     string               `json:"http_front,omitempty"`    // router (default) | apache
	Suffix    string               `json:"domain_suffix,omitempty"` // default TLD for new domains
	SitesDir  string               `json:"sites_dir,omitempty"`     // parent dir for new sites
	Inv       Inventory            `json:"inventory"`
	UpdatedAt time.Time            `json:"updated_at"`
}

func fileModTime(p string) time.Time {
	st, err := os.Stat(p)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// OpenStore loads (or creates) the store file.
func OpenStore() (*Store, error) {
	p := P()
	if err := p.Ensure(); err != nil {
		return nil, err
	}
	s := &Store{path: p.Store(), Data: storeData{
		Sites:     map[string]*Site{},
		Worktrees: map[string]*Worktree{},
	}}
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.Data); err != nil {
			return nil, fmt.Errorf("corrupt store %s: %w", s.path, err)
		}
		s.loadedAt = fileModTime(s.path)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	s.normalize()
	s.base = s.snapshot()
	return s, nil
}

func (s *Store) normalize() {
	if s.Data.Sites == nil {
		s.Data.Sites = map[string]*Site{}
	}
	if s.Data.Worktrees == nil {
		s.Data.Worktrees = map[string]*Worktree{}
	}
}

// lock takes the advisory store lock. Returns the release func.
func (s *Store) lock() func() {
	f, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() {}
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return func() {}
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}

// Save writes the store atomically under the lock (temp + rename).
//
// It is a three-way merge, not a blind overwrite. Five processes share this file
// — CLI, daemon, TUI, MCP server and any agent — and writing a whole in-memory
// snapshot silently erased whatever another one had changed since load: a
// setting written by the CLI would vanish the next time the daemon saved a site
// state. So the write is: re-read the file, apply only the fields this process
// actually changed, keep everything else as it is on disk.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release := s.lock()
	defer release()
	s.Data.UpdatedAt = time.Now()
	b, err := s.mergedBytes()
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.loadedAt = fileModTime(s.path)
	// What we just wrote is the new common ancestor for the next save, and its
	// deletions are now on disk.
	s.base = s.snapshot()
	s.deleted = nil
	return nil
}

// mergedBytes is the document to write: our changes layered onto whatever is on
// disk now. Caller holds mu and the file lock.
func (s *Store) mergedBytes() ([]byte, error) {
	mine, err := json.Marshal(s.Data)
	if err != nil {
		return nil, err
	}
	onDisk, err := os.ReadFile(s.path)
	if err != nil {
		// Nothing to merge with (first write, or the file went away).
		return json.MarshalIndent(s.Data, "", "  ")
	}
	merged, err := mergeStore(s.base, mine, onDisk, s.deleted)
	if err != nil {
		// A merge we cannot reason about must not lose this process's work:
		// fall back to our own document rather than writing nothing.
		return json.MarshalIndent(s.Data, "", "  ")
	}
	return merged, nil
}

// snapshot records the document as it stands, to serve as the ancestor a later
// save diffs against.
func (s *Store) snapshot() []byte {
	b, err := json.Marshal(s.Data)
	if err != nil {
		return nil
	}
	return b
}

// mergeStore is a field-level three-way merge over the store document.
// It works on generic JSON so a field added later is covered without anyone
// having to remember to update the merge.
//
//	base = the document this process loaded, mine = its current state,
//	disk = what is there now (possibly written by another process).
//
// A field this process changed wins. Anything else keeps the disk value. The two
// object maps (sites, worktrees) merge per entry, so one process adding a site
// while another deletes a different one keeps both intentions.
func mergeStore(base, mine, disk []byte, deleted map[string]map[string]bool) ([]byte, error) {
	var b, m, d map[string]json.RawMessage
	if err := json.Unmarshal(mine, &m); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(disk, &d); err != nil {
		return nil, err
	}
	if len(base) > 0 {
		if err := json.Unmarshal(base, &b); err != nil {
			return nil, err
		}
	}
	out := map[string]json.RawMessage{}
	// Start from disk: fields we never touched, including ones this build does
	// not know about, survive untouched.
	for k, v := range d {
		out[k] = v
	}
	for k, mv := range m {
		switch k {
		case "sites", "worktrees":
			merged, err := mergeEntries(b[k], mv, d[k], deleted[k])
			if err != nil {
				return nil, err
			}
			out[k] = merged
		case "updated_at":
			out[k] = mv
		default:
			if !bytes.Equal(mv, b[k]) {
				out[k] = mv // this process changed it
			}
		}
	}
	return json.MarshalIndent(out, "", "  ")
}

// mergeEntries merges one keyed object (sites or worktrees) entry by entry.
func mergeEntries(base, mine, disk json.RawMessage, deleted map[string]bool) (json.RawMessage, error) {
	b, m, d := map[string]json.RawMessage{}, map[string]json.RawMessage{}, map[string]json.RawMessage{}
	for raw, dst := range map[*json.RawMessage]*map[string]json.RawMessage{
		&base: &b, &mine: &m, &disk: &d,
	} {
		if len(*raw) == 0 || string(*raw) == "null" {
			continue
		}
		if err := json.Unmarshal(*raw, dst); err != nil {
			return nil, err
		}
	}
	out := map[string]json.RawMessage{}
	for k, v := range d {
		out[k] = v
	}
	for k, mv := range m {
		if bv, had := b[k]; !had || !bytes.Equal(mv, bv) {
			out[k] = mv // added or changed here
		}
	}
	// Only removals this process actually asked for. Inferring them from absence
	// meant a stale snapshot could erase live records.
	for k := range deleted {
		delete(out, k)
	}
	return json.Marshal(out)
}

// ReloadIfChanged re-reads the store file if its mtime advanced. Lets the
// daemon observe sites/worktrees/front changes the CLI made without IPC.
func (s *Store) ReloadIfChanged() {
	st, err := os.Stat(s.path)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loadedAt.IsZero() && !st.ModTime().After(s.loadedAt) {
		return
	}
	release := s.lock()
	defer release()
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var d storeData
	if err := json.Unmarshal(b, &d); err != nil {
		return
	}
	s.Data = d
	s.normalize()
	s.loadedAt = fileModTime(s.path)
	s.base = s.snapshot()
}

// Site fetches a site by slug.
func (s *Store) Site(slug string) *Site { return s.Data.Sites[slug] }

// Sites returns sites sorted by name.
func (s *Store) Sites() []*Site {
	out := make([]*Site, 0, len(s.Data.Sites))
	for _, v := range s.Data.Sites {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// FindSiteByDomain matches domain or alias.
func (s *Store) FindSiteByDomain(domain string) *Site {
	for _, site := range s.Data.Sites {
		if site.Domain == domain {
			return site
		}
		for _, a := range site.Aliases {
			if a == domain {
				return site
			}
		}
	}
	return nil
}

// PutSite inserts/updates a site.
func (s *Store) PutSite(site *Site) { s.Data.Sites[site.Slug] = site }

// DelSite removes a site row.
// DelSite removes a site and records that this process meant to. Merging used to
// infer deletions from "present when loaded, absent now", which turned any stale
// in-memory copy into a delete order: records vanished while their database,
// hosts entry and pool config stayed behind. Intent is now explicit.
func (s *Store) DelSite(slug string) {
	delete(s.Data.Sites, slug)
	s.markDeleted("sites", slug)
}

// WorktreesFor returns worktrees of a site sorted by branch.
func (s *Store) WorktreesFor(slug string) []*Worktree {
	var out []*Worktree
	for _, w := range s.Data.Worktrees {
		if w.Site == slug {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Branch < out[j].Branch })
	return out
}

// PutWorktree inserts/updates a worktree.
func (s *Store) PutWorktree(w *Worktree) { s.Data.Worktrees[w.ID] = w }

// DelWorktree removes a worktree row.
// DelWorktree removes a worktree, recording the intent for the same reason.
func (s *Store) DelWorktree(id string) {
	delete(s.Data.Worktrees, id)
	s.markDeleted("worktrees", id)
}

// markDeleted notes a deletion this process performed, so a merge can apply it
// without guessing. Callers hold no lock; the map is only read while saving.
func (s *Store) markDeleted(kind, key string) {
	if s.deleted == nil {
		s.deleted = map[string]map[string]bool{}
	}
	if s.deleted[kind] == nil {
		s.deleted[kind] = map[string]bool{}
	}
	s.deleted[kind][key] = true
}

// NextPorts scans sites for free http/https port pairs.
func (s *Store) NextPorts() (int, int) {
	used := map[int]bool{}
	for _, site := range s.Data.Sites {
		used[site.HTTPPort] = true
		used[site.HTTPSPort] = true
	}
	for p := DefaultHTTPPort + 1; ; p++ {
		if !used[p] && !used[p+1] {
			return p, p + 1
		}
	}
}

// Inventory returns the runtime inventory (mutable).
func (s *Store) Inventory() *Inventory { return &s.Data.Inv }

// DomainFree reports whether a domain is unused by any site/worktree.
func (s *Store) DomainFree(domain string) bool {
	if s.FindSiteByDomain(domain) != nil {
		return false
	}
	for _, w := range s.Data.Worktrees {
		if w.Domain == domain {
			return false
		}
	}
	return true
}

// AllDomains lists every served domain (sites + worktrees).
func (s *Store) AllDomains() []string {
	set := map[string]bool{}
	for _, site := range s.Data.Sites {
		set[site.Domain] = true
		for _, a := range site.Aliases {
			set[a] = true
		}
	}
	for _, w := range s.Data.Worktrees {
		set[w.Domain] = true
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// DefaultSuffix is the domain suffix new sites get. ".al" is short to type; note
// it is Albania's real ccTLD rather than a reserved one, so a local site called
// x.al shadows the real x.al for this machine. ".test" is the RFC 6761 reservation
// if that ever matters — agent-local suffix .test switches back.
const DefaultSuffix = ".al"

// Suffix returns the configured default domain suffix.
func (s *Store) Suffix() string {
	if s.Data.Suffix == "" {
		return DefaultSuffix
	}
	return s.Data.Suffix
}

// SetSuffix stores the default domain suffix (must start with a dot).
func (s *Store) SetSuffix(sfx string) error {
	if sfx == "" {
		sfx = DefaultSuffix
	}
	if !strings.HasPrefix(sfx, ".") {
		return fmt.Errorf("suffix must start with a dot, e.g. .test")
	}
	if !ValidDomain("x" + sfx) {
		return fmt.Errorf("invalid suffix %q", sfx)
	}
	s.Data.Suffix = sfx
	return s.Save()
}

// DefaultDomain builds a domain for a slug using the configured suffix.
func (s *Store) DefaultDomain(slug string) string { return slug + s.Suffix() }

// SitesDir is where new sites are created when no path is given. Default is our
// own tree; pointing it at somewhere like ~/Sites is the common case, so that
// checkouts sit where the rest of a person's work already lives.
func (s *Store) SitesDir() string {
	if strings.TrimSpace(s.Data.SitesDir) == "" {
		return P().Sites()
	}
	return s.Data.SitesDir
}

// SetSitesDir stores the default parent directory for new sites. It is created
// if missing so the setting cannot be accepted and then fail on first use. An
// empty value restores the default.
func (s *Store) SetSitesDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		s.Data.SitesDir = ""
		return s.Save()
	}
	abs, err := ResolveDir(dir)
	if err != nil {
		return err
	}
	if st, err := os.Stat(abs); err == nil && !st.IsDir() {
		return fmt.Errorf("%s is a file, not a directory", abs)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return err
	}
	s.Data.SitesDir = abs
	return s.Save()
}

// SiteDirFor is where a slug's checkout goes by default.
func (s *Store) SiteDirFor(slug string) string { return filepath.Join(s.SitesDir(), slug) }

// SanitizeName validates a site name for slug/domain use.
func SanitizeName(name string) (string, error) {
	slug := Slugify(name)
	if slug == "" {
		return "", fmt.Errorf("name %q has no usable characters", name)
	}
	if len(slug) > 48 {
		return "", fmt.Errorf("name too long (max 48): %q", slug)
	}
	return slug, nil
}

// ValidDomain does a light sanity check on a local domain.
func ValidDomain(d string) bool {
	if d == "" || strings.ContainsAny(d, " \t/\\:*?\"<>|") {
		return false
	}
	return strings.Contains(d, ".")
}
