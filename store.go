package main

import (
	"encoding/json"
	"fmt"
	"os"
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
	Data     storeData
}

type storeData struct {
	Sites     map[string]*Site     `json:"sites"`
	Worktrees map[string]*Worktree `json:"worktrees"`
	Front     string               `json:"http_front,omitempty"`    // router (default) | apache
	Suffix    string               `json:"domain_suffix,omitempty"` // default TLD for new domains
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
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release := s.lock()
	defer release()
	s.Data.UpdatedAt = time.Now()
	b, err := json.MarshalIndent(s.Data, "", "  ")
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
	return nil
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
func (s *Store) DelSite(slug string) { delete(s.Data.Sites, slug) }

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
func (s *Store) DelWorktree(id string) { delete(s.Data.Worktrees, id) }

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

// Suffix returns the configured default domain suffix.
func (s *Store) Suffix() string {
	if s.Data.Suffix == "" {
		return ".test"
	}
	return s.Data.Suffix
}

// SetSuffix stores the default domain suffix (must start with a dot).
func (s *Store) SetSuffix(sfx string) error {
	if sfx == "" {
		sfx = ".test"
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
