package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// JobStatus is the lifecycle of one long-running operation.
type JobStatus string

const (
	JobRunning JobStatus = "running"
	JobOK      JobStatus = "ok"
	JobErr     JobStatus = "error"
)

const jobRetain = 50

// JobStep is one progress line from a running operation.
type JobStep struct {
	Stage  string    `json:"stage"`
	Detail string    `json:"detail"`
	At     time.Time `json:"at"`
}

// Job is one create/import/search-replace (or anything else that takes seconds).
// The daemon keeps a ring of recent jobs so agents can poll instead of holding
// an HTTP socket for the whole dump.
type Job struct {
	mu         sync.Mutex
	ID         string
	Op         string
	Status     JobStatus
	Steps      []JobStep
	Result     any
	Error      string
	StartedAt  time.Time
	FinishedAt *time.Time
	done       chan struct{}
}

// JobView is a mutex-free snapshot for JSON and tests.
type JobView struct {
	ID         string     `json:"id"`
	Op         string     `json:"op"`
	Status     JobStatus  `json:"status"`
	Steps      []JobStep  `json:"steps"`
	Result     any        `json:"result,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Snapshot is a copy safe to marshal after the job has moved on.
func (j *Job) Snapshot() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	steps := make([]JobStep, len(j.Steps))
	copy(steps, j.Steps)
	var finished *time.Time
	if j.FinishedAt != nil {
		t := *j.FinishedAt
		finished = &t
	}
	return JobView{
		ID: j.ID, Op: j.Op, Status: j.Status, Steps: steps,
		Result: j.Result, Error: j.Error, StartedAt: j.StartedAt, FinishedAt: finished,
	}
}

// Wait blocks until the job finishes.
func (j *Job) Wait() { <-j.done }

// running reports whether the job has not finished yet.
func (j *Job) running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.Status == JobRunning
}

func (j *Job) progress(stage, detail string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Steps = append(j.Steps, JobStep{Stage: stage, Detail: detail, At: time.Now()})
	if len(j.Steps) > 200 {
		j.Steps = j.Steps[len(j.Steps)-200:]
	}
}

func (j *Job) finish(result any, err error) {
	j.mu.Lock()
	now := time.Now()
	j.FinishedAt = &now
	if err != nil {
		j.Status = JobErr
		j.Error = err.Error()
	} else {
		j.Status = JobOK
		j.Result = result
	}
	j.mu.Unlock()
	close(j.done)
}

// JobHub is the daemon's in-memory job catalogue.
type JobHub struct {
	mu    sync.Mutex
	jobs  map[string]*Job
	order []string
}

// NewJobHub builds an empty hub.
func NewJobHub() *JobHub { return &JobHub{jobs: map[string]*Job{}} }

// DrainRunning blocks until no job is running or timeout elapses, giving an
// in-flight import/deploy a chance to finish before the process that started
// it exits, instead of leaving it silently truncated mid-operation. This is
// a bounded best-effort wait, not cancellation: Start's fn has no way to be
// told to stop, since threading a context through every job callback across
// the codebase would be a much larger, riskier change than the daemon
// shutdown path calling for.
func (h *JobHub) DrainRunning(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !h.anyRunning() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (h *JobHub) anyRunning() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, j := range h.jobs {
		if j.running() {
			return true
		}
	}
	return false
}

// Start runs fn in the background and returns the job immediately.
func (h *JobHub) Start(op string, fn func(cb func(stage, detail string)) (any, error)) *Job {
	j := &Job{
		ID:        newJobID(),
		Op:        op,
		Status:    JobRunning,
		StartedAt: time.Now(),
		done:      make(chan struct{}),
	}
	h.mu.Lock()
	h.jobs[j.ID] = j
	h.order = append(h.order, j.ID)
	// Trim the oldest finished jobs past the retention cap. A job still
	// running stays whatever its age: evicting by start order alone made a
	// long import vanish from get_job while it was still working, once fifty
	// quicker calls had started behind it.
	for i := 0; len(h.order) > jobRetain && i < len(h.order); {
		old := h.jobs[h.order[i]]
		if old != nil && old.running() {
			i++
			continue
		}
		delete(h.jobs, h.order[i])
		h.order = append(h.order[:i], h.order[i+1:]...)
	}
	h.mu.Unlock()
	go func() {
		var res any
		var err error
		defer func() {
			if rec := recover(); rec != nil {
				err = fmt.Errorf("panic: %v", rec)
			}
			j.finish(res, err)
		}()
		res, err = fn(j.progress)
	}()
	return j
}

// Get returns a job by id.
func (h *JobHub) Get(id string) *Job {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.jobs[id]
}

// List returns newest-first snapshots.
func (h *JobHub) List() []JobView {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]JobView, 0, len(h.order))
	for i := len(h.order) - 1; i >= 0; i-- {
		if j := h.jobs[h.order[i]]; j != nil {
			out = append(out, j.Snapshot())
		}
	}
	return out
}

func newJobID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
