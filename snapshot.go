package main

import (
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Snapshots are per-site database save-points: a gzipped logical dump taken
// automatically before something destructive, or whenever the user asks.
// They exist because "working through a migration" and "about to load a
// production dump" are both one command away from losing local state that
// exists nowhere else. A snapshot is a plain .sql.gz, so even without this
// app it is just a dump; restores stream through the same loader as imports,
// so size does not matter.

// snapshotKeepAuto is how many automatic save-points survive per site.
// Manual (named) snapshots are never pruned — the user asked for those.
const snapshotKeepAuto = 5

// autoSnapshotPrefix marks snapshots the app took on its own initiative.
// It is part of the label, so `20260901-153000-auto-import` reads as what
// happened, and pruning can tell the app's snapshots from the user's.
const autoSnapshotPrefix = "auto-"

// snapshotTimeLayout is the leading, sortable timestamp in every snapshot name.
const snapshotTimeLayout = "20060102-150405"

// SnapshotsDir is where a site's snapshots live. It survives deleting the
// site deliberately: the snapshot taken before `delete` is the way back
// (`agent-local db <new-slug> import <snapshot path>`).
func (p Paths) SnapshotsDir(slug string) string {
	return filepath.Join(p.Root, "snapshots", slug)
}

// SnapshotInfo describes one saved snapshot.
type SnapshotInfo struct {
	Name      string    `json:"name"` // restore key: the file name without .sql.gz
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
	Auto      bool      `json:"auto"`
}

// SnapshotDB saves a snapshot of a site's database. The label is optional
// colour ("pre-migration"); the returned timestamped name is what restore
// takes either way.
func (e *Engine) SnapshotDB(slug, label string) (*SnapshotInfo, error) {
	site := e.Store.Site(slug)
	if site == nil {
		return nil, fmt.Errorf("no such site: %s", slug)
	}
	if err := e.EnsureDB(); err != nil {
		return nil, err
	}
	dir := P().SnapshotsDir(slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	name := time.Now().Format(snapshotTimeLayout)
	if label != "" {
		name += "-" + Slugify(label)
	}
	// Two snapshots in the same second (a test suite, an agent) must not
	// silently truncate each other.
	path := filepath.Join(dir, name+".sql.gz")
	for i := 2; fileExists(path); i++ {
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.sql.gz", name, i))
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return nil, err
	}
	gz := gzip.NewWriter(f)
	err = e.dumpDB(site, gz)
	if cerr := gz.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		// A half-written snapshot restoring as half a database is worse
		// than no snapshot at all.
		os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(filepath.Base(path), ".sql.gz")
	return &SnapshotInfo{
		Name:      base,
		Path:      path,
		Size:      fi.Size(),
		CreatedAt: fi.ModTime(),
		Auto:      snapshotIsAuto(base),
	}, nil
}

// Snapshots lists a site's snapshots, newest first.
func (e *Engine) Snapshots(slug string) ([]SnapshotInfo, error) {
	dir := P().SnapshotsDir(slug)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []SnapshotInfo{}, nil
		}
		return nil, err
	}
	out := []SnapshotInfo{}
	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".sql.gz") {
			continue
		}
		fi, err := ent.Info()
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(ent.Name(), ".sql.gz")
		out = append(out, SnapshotInfo{
			Name:      name,
			Path:      filepath.Join(dir, ent.Name()),
			Size:      fi.Size(),
			CreatedAt: snapshotTime(name, fi.ModTime()),
			Auto:      snapshotIsAuto(name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// snapshotIsAuto reports whether a snapshot name marks an automatic
// save-point: `<date>-<time>-auto-<reason>`.
func snapshotIsAuto(name string) bool {
	parts := strings.SplitN(name, "-", 3)
	return len(parts) == 3 && strings.HasPrefix(parts[2], autoSnapshotPrefix)
}

// snapshotTime reads the timestamp a snapshot name leads with, falling back
// to the file's mtime for a file someone renamed or dropped in by hand.
func snapshotTime(name string, fallback time.Time) time.Time {
	if len(name) >= len(snapshotTimeLayout) {
		if t, err := time.ParseInLocation(snapshotTimeLayout, name[:len(snapshotTimeLayout)], time.Local); err == nil {
			return t
		}
	}
	return fallback
}

// RestoreSnapshot loads a snapshot back into the site's database, replacing
// current contents. name "" means the newest. URLs are left exactly as
// dumped — a snapshot came from this same site, so rewriting could only do
// damage. backup saves a pre-restore snapshot first, so a mis-aimed restore
// is itself restorable.
func (e *Engine) RestoreSnapshot(slug, name string, backup bool) (string, error) {
	site := e.Store.Site(slug)
	if site == nil {
		return "", fmt.Errorf("no such site: %s", slug)
	}
	snaps, err := e.Snapshots(slug)
	if err != nil {
		return "", err
	}
	if len(snaps) == 0 {
		return "", fmt.Errorf("no snapshots for %s (take one: agent-local db %s snapshot)", slug, slug)
	}
	// Resolve the target before any pre-restore snapshot is taken, or
	// "the newest" would resolve to the backup of the state being replaced.
	var pick *SnapshotInfo
	if name == "" {
		pick = &snaps[0]
	} else {
		for i := range snaps {
			if snaps[i].Name == name {
				pick = &snaps[i]
				break
			}
		}
		if pick == nil {
			names := make([]string, 0, len(snaps))
			for _, s := range snaps {
				names = append(names, s.Name)
			}
			return "", fmt.Errorf("no snapshot %q for %s; have: %s", name, slug, strings.Join(names, ", "))
		}
	}
	msg := ""
	var preRestorePath string
	if backup {
		took, err := e.autoSnapshot(slug, "restore")
		if err != nil {
			return "", fmt.Errorf("pre-restore snapshot: %w (--no-snapshot / no_snapshot skips it)", err)
		}
		if took != "" {
			msg = "saved " + took + ", "
			preRestorePath = filepath.Join(P().SnapshotsDir(slug), took+".sql.gz")
		}
	}
	if err := e.ResetDB(slug); err != nil {
		return "", err
	}
	if err := e.loadSQLFile(site, pick.Path); err != nil {
		if preRestorePath != "" {
			if rerr := e.rollbackLoad(slug, site, preRestorePath); rerr != nil {
				return "", fmt.Errorf("restore %s failed: %w; auto-restore of pre-restore snapshot also failed: %v", pick.Name, err, rerr)
			}
			return "", fmt.Errorf("restore %s failed: %w; automatically rolled back to the pre-restore snapshot", pick.Name, err)
		}
		return "", err
	}
	return fmt.Sprintf("%srestored %s into %s (%d tables)", msg, pick.Name, site.DBName, e.tableCount(site)), nil
}

// rollbackLoad resets the database and reloads a known-good snapshot. Used
// to recover from a failed load: best-effort, since it can only fail closed,
// not undo whatever partial state the failed attempt left behind.
func (e *Engine) rollbackLoad(slug string, site *Site, path string) error {
	if err := e.ResetDB(slug); err != nil {
		return err
	}
	return e.loadSQLFile(site, path)
}

// ResetDBBackup empties a site's database the way `db reset` means it: with
// an automatic snapshot first, unless the caller opted out.
func (e *Engine) ResetDBBackup(slug string, snapshot bool) (string, error) {
	site := e.Store.Site(slug)
	if site == nil {
		return "", fmt.Errorf("no such site: %s", slug)
	}
	msg := ""
	if snapshot {
		took, err := e.autoSnapshot(slug, "reset")
		if err != nil {
			return "", fmt.Errorf("pre-reset snapshot: %w (--no-snapshot / no_snapshot skips it)", err)
		}
		if took != "" {
			msg = "saved " + took + ", "
		}
	}
	if err := e.ResetDB(slug); err != nil {
		return "", err
	}
	return msg + "database emptied: " + site.DBName, nil
}

// autoSnapshot is the safety net in front of destructive database operations:
// import, reset, restore, delete. An empty database is not worth saving; a
// snapshot that fails is a reason to stop, because a safety net that silently
// misses is worse than none. Automatic save-points are pruned to the newest
// few; named ones are kept.
func (e *Engine) autoSnapshot(slug, reason string) (string, error) {
	site := e.Store.Site(slug)
	if site == nil {
		return "", fmt.Errorf("no such site: %s", slug)
	}
	if err := e.EnsureDB(); err != nil {
		return "", err
	}
	if e.tableCount(site) == 0 {
		return "", nil
	}
	snap, err := e.SnapshotDB(slug, autoSnapshotPrefix+reason)
	if err != nil {
		return "", err
	}
	e.pruneAutoSnapshots(slug, snapshotKeepAuto)
	return snap.Name, nil
}

// pruneAutoSnapshots keeps the newest n automatic snapshots and deletes the
// rest. Best-effort: pruning failing must not fail the operation it rode in on.
func (e *Engine) pruneAutoSnapshots(slug string, keep int) {
	snaps, err := e.Snapshots(slug)
	if err != nil {
		return
	}
	autos := 0
	for _, s := range snaps { // newest first
		if !s.Auto {
			continue
		}
		autos++
		if autos > keep {
			os.Remove(s.Path)
		}
	}
}

// humanBytes renders a size the way a directory listing would.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
