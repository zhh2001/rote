// Package engine schedules jobs and records each run to the store.
package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhh2001/rote/internal/config"
	"github.com/zhh2001/rote/internal/runner"
	"github.com/zhh2001/rote/internal/scheduler"
	"github.com/zhh2001/rote/internal/store"
)

// hookTimeout bounds how long an on_failure hook may run.
const hookTimeout = 30 * time.Second

// schedule reports the next activation after a given instant; the zero time
// means "never again". It is satisfied by scheduler.Schedule (and, in tests, by
// lightweight fakes), keeping the time source pluggable without a public knob.
type schedule interface {
	Next(after time.Time) time.Time
}

type jobEntry struct {
	cfg     config.Job
	sched   schedule
	running atomic.Bool // true while a run (or its hook) is in flight
}

// Engine runs configured jobs on their schedules and records the outcomes.
type Engine struct {
	entries []*jobEntry
	store   *store.Store
	logger  *slog.Logger
	wg      sync.WaitGroup
}

// New builds an Engine, parsing each job's schedule up front; a parse failure
// aborts construction. A nil logger discards all output.
func New(jobs []config.Job, st *store.Store, logger *slog.Logger) (*Engine, error) {
	entries := make([]*jobEntry, 0, len(jobs))
	for _, j := range jobs {
		sched, err := scheduler.Parse(j.Schedule)
		if err != nil {
			return nil, fmt.Errorf("engine: job %q: %w", j.Name, err)
		}
		entries = append(entries, &jobEntry{cfg: j, sched: sched})
	}
	return newEngine(entries, st, logger), nil
}

// newEngine assembles an Engine from prebuilt entries. It exists so tests can
// inject deterministic schedules without widening the public API.
func newEngine(entries []*jobEntry, st *store.Store, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Engine{entries: entries, store: st, logger: logger}
}

// Run starts one scheduling goroutine per job and blocks until ctx is canceled.
// On cancellation it stops scheduling new runs and waits for any in-flight runs
// (and their on_failure hooks) to finish on their own before returning nil.
//
// In-flight runs are deliberately NOT canceled by shutdown: each is bounded only
// by its own Timeout. A job with Timeout == 0 whose command never exits will
// therefore hold up shutdown indefinitely; set a Timeout to avoid this. A forced
// second-stage exit is left to the caller.
func (e *Engine) Run(ctx context.Context) error {
	for _, j := range e.entries {
		e.wg.Add(1)
		go e.schedule(ctx, j)
	}
	e.wg.Wait()
	return nil
}

// schedule drives one job: it repeatedly computes the next wall-clock activation
// and waits for it, dispatching a run when it arrives. The next time is always
// computed from the current clock (not from when the previous run finished), so
// a slow or overrunning command never shifts the schedule.
func (e *Engine) schedule(ctx context.Context, j *jobEntry) {
	defer e.wg.Done()
	for {
		next := j.sched.Next(time.Now())
		if next.IsZero() {
			e.logger.Info("job has no further runs; stopping schedule", "job", j.cfg.Name)
			return
		}

		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		// Overlap policy: skip if the previous run is still in flight. The
		// atomic CompareAndSwap is the sole gate, so the scheduling and execution
		// goroutines stay race-free.
		if !j.running.CompareAndSwap(false, true) {
			e.logger.Warn("previous run still in progress; skipping", "job", j.cfg.Name)
			continue
		}

		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			defer j.running.Store(false)
			e.execute(j)
		}()
	}
}

// execute runs the command, records the result, and fires the failure hook.
func (e *Engine) execute(j *jobEntry) {
	// A detached background context: the run is bounded only by its own Timeout,
	// so shutting the engine down does not kill a run already in flight.
	res := runner.Run(context.Background(), runner.Spec{
		Name:    j.cfg.Name,
		Command: j.cfg.Command,
		Timeout: j.cfg.Timeout,
	})

	rec := store.Run{
		JobName:         j.cfg.Name,
		StartedAt:       res.StartedAt,
		FinishedAt:      res.FinishedAt,
		Duration:        res.Duration,
		ExitCode:        res.ExitCode,
		TimedOut:        res.TimedOut,
		Success:         res.Success(),
		Stdout:          res.Stdout,
		Stderr:          res.Stderr,
		StdoutTruncated: res.StdoutTruncated,
		StderrTruncated: res.StderrTruncated,
		Err:             errString(res.Err),
	}
	if _, err := e.store.Insert(context.Background(), rec); err != nil {
		e.logger.Error("failed to record run", "job", j.cfg.Name, "err", err)
	}

	if !res.Success() && j.cfg.OnFailure != "" {
		e.runHook(j)
	}
}

// runHook makes a best-effort attempt to run the job's on_failure command. Its
// outcome is logged but never recorded in the store.
func (e *Engine) runHook(j *jobEntry) {
	res := runner.Run(context.Background(), runner.Spec{
		Name:    j.cfg.Name + ":on_failure",
		Command: j.cfg.OnFailure,
		Timeout: hookTimeout,
	})
	if !res.Success() {
		e.logger.Error("on_failure hook failed", "job", j.cfg.Name,
			"exit_code", res.ExitCode, "err", errString(res.Err))
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
