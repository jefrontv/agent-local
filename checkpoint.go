package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Checkpoints are whole-tree save-points: a snapshot of wp-content (or the
// whole docroot) plus, best-effort, a matching database snapshot, all under
// one restorable name. They exist for the moment before an agent runs a
// risky migration or plugin update and wants a single "put it back" command
// that covers code and data together, not just the database half that
// SnapshotDB already covers on its own.

// checkpointTimeLayout is the leading, sortable timestamp in every checkpoint
// name, matching snapshotTimeLayout so the two sort the same way.
const checkpointTimeLayout = "20060102-150405"

// checkpointLabelRe matches what survives label sanitisation: lowercase
// letters, digits, and hyphens only, so a checkpoint name is always a safe
// path component on every filesystem this app targets.
var checkpointLabelRe = regexp.MustCompile(`[^a-z0-9]+`)

// CheckpointsDir is where a site's checkpoints live: <root>/checkpoints/<slug>.
// It survives deleting the site, same as SnapshotsDir, since a checkpoint
// taken before a destructive experiment is exactly what you'd reach for
// after redoing the site from scratch.
func (p Paths) CheckpointsDir(slug string) string {
	return filepath.Join(p.Root, "checkpoints", slug)
}

// CheckpointInfo describes one saved checkpoint.
type CheckpointInfo struct {
	Name       string    `json:"name"`
	Label      string    `json:"label"`
	CreatedAt  time.Time `json:"created_at"`
	Scope      string    `json:"scope"`             // "wp-content" | "all"
	DBSnapshot string    `json:"db_snapshot"`       // SnapshotInfo.Name, or "" if none was taken
	FilesPath  string    `json:"files_path"`        // <dir>/<name>/files
	SizeHint   string    `json:"size_hint"`         // "clone" (APFS clonefile) or "copy" (fell back)
	Path       string    `json:"path"`              // <dir>/<name>
	Warning    string    `json:"warning,omitempty"` // non-fatal problem taking the checkpoint (e.g. DB down)
}

// sanitizeCheckpointLabel lowercases a label and strips it down to
// [a-z0-9-], trimmed and capped at 40 characters, so it is always safe to
// append to a timestamp as a directory name.
func sanitizeCheckpointLabel(label string) string {
	s := checkpointLabelRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(label)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

// checkpointScopeDir resolves which directory a scope covers.
func checkpointScopeDir(site *Site, scope string) (string, error) {
	switch scope {
	case "", "wp-content":
		return filepath.Join(site.WPDir, "wp-content"), nil
	case "all":
		return site.WPDir, nil
	default:
		return "", fmt.Errorf("scope must be wp-content or all, got %q", scope)
	}
}

// cloneTree copies src to dst, preferring an APFS clonefile (`cp -c -R`):
// near-instant and near-zero extra disk regardless of tree size, which
// matters because wp-content on a real site can be gigabytes of uploads.
// A filesystem that doesn't support clonefile (non-APFS, cross-volume)
// fails cp -c; the fallback is a plain recursive copy.
func cloneTree(src, dst string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := exec.Command("cp", "-c", "-R", src, dst).Run(); err == nil {
		return "clone", nil
	}
	os.RemoveAll(dst)
	if err := exec.Command("cp", "-R", src, dst).Run(); err != nil {
		return "", fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return "copy", nil
}

// Checkpoint saves a whole-tree save-point: a clone of the scope directory
// plus, best-effort, a database snapshot. The database step is allowed to
// fail on its own (a stopped DB shouldn't make the files any less worth
// saving) — its error is recorded as a warning instead of aborting.
func (e *Engine) Checkpoint(slug, label, scope string) (*CheckpointInfo, error) {
	site := e.Store.Site(slug)
	if site == nil {
		return nil, fmt.Errorf("no such site: %s", slug)
	}
	if scope == "" {
		scope = "wp-content"
	}
	src, err := checkpointScopeDir(site, scope)
	if err != nil {
		return nil, err
	}
	if !fileExists(src) {
		return nil, fmt.Errorf("nothing to checkpoint: %s does not exist", src)
	}

	name := time.Now().Format(checkpointTimeLayout)
	if l := sanitizeCheckpointLabel(label); l != "" {
		name += "-" + l
	}
	dir := filepath.Join(P().CheckpointsDir(slug), name)
	for i := 2; fileExists(dir); i++ {
		dir = filepath.Join(P().CheckpointsDir(slug), fmt.Sprintf("%s-%d", name, i))
		name = filepath.Base(dir)
	}
	filesPath := filepath.Join(dir, "files")

	method, err := cloneTree(src, filesPath)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}

	info := &CheckpointInfo{
		Name:      name,
		Label:     label,
		CreatedAt: time.Now(),
		Scope:     scope,
		FilesPath: filesPath,
		SizeHint:  method,
		Path:      dir,
	}

	if snap, err := e.SnapshotDB(slug, "checkpoint-"+name); err != nil {
		info.Warning = "database snapshot failed: " + err.Error()
	} else {
		info.DBSnapshot = snap.Name
	}

	metaBytes, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	// meta.json is written last: a checkpoint dir without it is incomplete
	// and ListCheckpoints skips it, so a crash mid-checkpoint never surfaces
	// a half-written save-point as restorable.
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), metaBytes, 0o644); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return info, nil
}

// ListCheckpoints lists a site's checkpoints, newest first. A directory
// without meta.json (interrupted mid-write) is treated as absent.
func (e *Engine) ListCheckpoints(slug string) ([]CheckpointInfo, error) {
	dir := P().CheckpointsDir(slug)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []CheckpointInfo{}, nil
		}
		return nil, err
	}
	out := []CheckpointInfo{}
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		info, err := loadCheckpointMeta(dir, ent.Name())
		if err != nil {
			continue
		}
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// loadCheckpointMeta reads one checkpoint's meta.json by name.
func loadCheckpointMeta(dir, name string) (*CheckpointInfo, error) {
	b, err := os.ReadFile(filepath.Join(dir, name, "meta.json"))
	if err != nil {
		return nil, err
	}
	var info CheckpointInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// RollbackReport describes what a rollback did.
type RollbackReport struct {
	Name          string `json:"name"`
	DBRestored    bool   `json:"db_restored"`
	DBMessage     string `json:"db_message"`
	FilesRestored bool   `json:"files_restored"`
	PreviousFiles string `json:"previous_files"`
	Restarted     bool   `json:"restarted"`
}

// rollbackFiles swaps the current scope directory for the checkpoint's saved
// one. The current tree is renamed aside (same volume, atomic — a rename can't
// partially fail the way a copy-then-delete could) rather than deleted, so a
// bad checkpoint never destroys the one copy of whatever was about to be
// replaced.
func (e *Engine) rollbackFiles(site *Site, meta CheckpointInfo) (string, error) {
	scopeDir, err := checkpointScopeDir(site, meta.Scope)
	if err != nil {
		return "", err
	}
	stamp := time.Now().Format(checkpointTimeLayout)
	previous := filepath.Join(meta.Path, "pre-rollback-"+stamp)
	if fileExists(scopeDir) {
		if err := os.Rename(scopeDir, previous); err != nil {
			// A site on another volume (an external drive) cannot be renamed
			// into ~/.agent-local; set it aside next to itself instead, where
			// a rename is always possible.
			previous = scopeDir + ".pre-rollback-" + stamp
			if err := os.Rename(scopeDir, previous); err != nil {
				return "", fmt.Errorf("set aside current %s: %w", scopeDir, err)
			}
		}
	} else {
		previous = ""
	}
	if _, err := cloneTree(meta.FilesPath, scopeDir); err != nil {
		// The clone that was supposed to become the new state failed; put
		// the original back rather than leave the site with neither.
		if previous != "" {
			os.Rename(previous, scopeDir)
		}
		return "", err
	}
	return previous, nil
}

// Rollback restores a site to a checkpoint: the database (if the checkpoint
// has one), then the files, then a pool restart so PHP-FPM's opcode cache
// and any in-process state pick up the restored code. Nothing is ever
// deleted — the pre-rollback files move aside instead of being removed.
func (e *Engine) Rollback(slug, name string) (*RollbackReport, error) {
	site := e.Store.Site(slug)
	if site == nil {
		return nil, fmt.Errorf("no such site: %s", slug)
	}
	if !validCheckpointName(name) {
		return nil, fmt.Errorf("bad checkpoint name: %q", name)
	}
	meta, err := loadCheckpointMeta(P().CheckpointsDir(slug), name)
	if err != nil {
		return nil, fmt.Errorf("no checkpoint %q for %s", name, slug)
	}

	report := &RollbackReport{Name: name}

	if meta.DBSnapshot != "" {
		msg, err := e.RestoreSnapshot(slug, meta.DBSnapshot, true)
		if err != nil {
			return nil, fmt.Errorf("restore database: %w", err)
		}
		report.DBRestored = true
		report.DBMessage = msg
	}

	previous, err := e.rollbackFiles(site, *meta)
	if err != nil {
		return nil, fmt.Errorf("restore files: %w", err)
	}
	report.FilesRestored = true
	report.PreviousFiles = previous

	// A running pool is restarted so opcache and any in-process state pick up
	// the restored code. A stopped site stays stopped: a rollback is not a
	// request to start serving.
	if e.FPMAlive(slug) {
		e.StopSite(slug)
		if err := e.StartSite(slug); err == nil {
			report.Restarted = true
		}
	}

	return report, nil
}

// validCheckpointName is what may follow the checkpoints directory in a path:
// a name we generated, never a traversal. Checked before any name reaches
// the filesystem, since a name is the one input a caller controls here.
func validCheckpointName(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/\\") && !strings.Contains(name, "..")
}

// DeleteCheckpoint removes a checkpoint's directory. The name is checked
// against ListCheckpoints first, so a typo or a traversal attempt can't
// reach outside the checkpoints directory.
func (e *Engine) DeleteCheckpoint(slug, name string) error {
	if !validCheckpointName(name) {
		return fmt.Errorf("bad checkpoint name: %q", name)
	}
	list, err := e.ListCheckpoints(slug)
	if err != nil {
		return err
	}
	found := false
	for _, c := range list {
		if c.Name == name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("no checkpoint %q for %s", name, slug)
	}
	return os.RemoveAll(filepath.Join(P().CheckpointsDir(slug), name))
}

// --- daemon HTTP handlers ---

type checkpointReq struct {
	Label string `json:"label"`
	Scope string `json:"scope"`
}

// handleCheckpoint saves a new checkpoint for a site.
func (a *APIServer) handleCheckpoint(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	var req checkpointReq
	json.NewDecoder(r.Body).Decode(&req)
	info, err := a.engine.Checkpoint(site.Slug, req.Label, req.Scope)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, info)
}

// handleListCheckpoints lists a site's checkpoints, newest first.
func (a *APIServer) handleListCheckpoints(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	list, err := a.engine.ListCheckpoints(site.Slug)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, list)
}

// handleRollback restores a site to a named checkpoint.
func (a *APIServer) handleRollback(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	report, err := a.engine.Rollback(site.Slug, r.PathValue("name"))
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, report)
}

// handleDeleteCheckpoint removes one checkpoint.
func (a *APIServer) handleDeleteCheckpoint(w http.ResponseWriter, r *http.Request) {
	site := a.requireSite(w, r)
	if site == nil {
		return
	}
	if err := a.engine.DeleteCheckpoint(site.Slug, r.PathValue("name")); err != nil {
		fail(w, 500, err.Error())
		return
	}
	ok(w, "deleted")
}
