package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/zhh2001/rote/internal/config"
	"github.com/zhh2001/rote/internal/engine"
	"github.com/zhh2001/rote/internal/runner"
	"github.com/zhh2001/rote/internal/scheduler"
	"github.com/zhh2001/rote/internal/store"
)

// previewLimit caps how many bytes of a stream are echoed in a run summary.
const previewLimit = 4096

// cmdStart builds the engine and runs it until ctx is canceled.
func cmdStart(ctx context.Context, logger *slog.Logger, jobs []config.Job, st *store.Store) error {
	eng, err := engine.New(jobs, st, logger)
	if err != nil {
		return err
	}
	logger.Info("starting scheduler", "jobs", len(jobs))
	return eng.Run(ctx)
}

// Exit codes for cmdRun, mirroring common shell/timeout conventions.
const (
	exitTimedOut  = 124 // job exceeded its timeout (as GNU timeout)
	exitNotRun    = 126 // job was killed or could not start
	exitNoSuchJob = 127 // job name not found in config
)

// cmdRun executes a single named job once, records it, writes a summary, and
// returns the process exit code to use. A non-nil error is an operational
// failure (its code is also returned); the job's own non-zero exit is reported
// through the returned code with a nil error.
func cmdRun(ctx context.Context, w io.Writer, jobs []config.Job, st *store.Store, jobName string) (int, error) {
	job, ok := findJob(jobs, jobName)
	if !ok {
		return exitNoSuchJob, fmt.Errorf("job %q not found; available jobs: %s", jobName, availableNames(jobs))
	}

	res := runner.Run(ctx, runner.Spec{
		Name:    job.Name,
		Command: job.Command,
		Timeout: job.Timeout,
	})

	// Record with a detached context so a canceled run is still persisted.
	if _, err := st.Insert(context.Background(), toRecord(job.Name, res)); err != nil {
		return 1, fmt.Errorf("recording run: %w", err)
	}

	writeRunSummary(w, job.Name, res)
	return runExitCode(res), nil
}

// runExitCode maps a run result to a process exit code.
func runExitCode(res runner.Result) int {
	switch {
	case res.TimedOut:
		return exitTimedOut
	case res.ExitCode == -1:
		return exitNotRun
	default:
		return res.ExitCode
	}
}

// cmdList prints each job with its next scheduled run and last recorded result.
func cmdList(ctx context.Context, w io.Writer, jobs []config.Job, st *store.Store, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSCHEDULE\tNEXT\tLAST")

	for _, j := range jobs {
		sched, err := scheduler.Parse(j.Schedule)
		if err != nil {
			return fmt.Errorf("job %q: %w", j.Name, err)
		}

		next := "never"
		if n := sched.Next(now); !n.IsZero() {
			next = "in " + shortDur(n.Sub(now))
		}

		last := "-"
		lr, ok, err := st.LastRun(ctx, j.Name)
		if err != nil {
			return fmt.Errorf("job %q: %w", j.Name, err)
		}
		if ok {
			last = fmt.Sprintf("%s %s ago (%s)", statusSymbol(lr.Success), shortDur(now.Sub(lr.StartedAt)), shortDur(lr.Duration))
		}

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", j.Name, j.Schedule, next, last)
	}
	return tw.Flush()
}

// cmdLogs prints the most recent runs for a job, optionally including the last
// run's captured output.
func cmdLogs(ctx context.Context, w io.Writer, st *store.Store, jobName string, n int, showOutput bool) error {
	if n <= 0 {
		n = 20
	}
	runs, err := st.RecentRuns(ctx, jobName, n)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Fprintf(w, "no runs recorded for %q\n", jobName)
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tSTATUS\tEXIT\tDURATION")
	for _, r := range runs {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n",
			r.StartedAt.Local().Format("2006-01-02 15:04:05"),
			statusSymbol(r.Success), r.ExitCode, shortDur(r.Duration))
	}
	tw.Flush()

	if showOutput {
		latest := runs[0] // RecentRuns is newest-first.
		writeStream(w, "last stdout", latest.Stdout, latest.StdoutTruncated)
		writeStream(w, "last stderr", latest.Stderr, latest.StderrTruncated)
	}
	return nil
}

func findJob(jobs []config.Job, name string) (config.Job, bool) {
	for _, j := range jobs {
		if j.Name == name {
			return j, true
		}
	}
	return config.Job{}, false
}

func availableNames(jobs []config.Job) string {
	if len(jobs) == 0 {
		return "(none)"
	}
	names := make([]string, len(jobs))
	for i, j := range jobs {
		names[i] = j.Name
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func toRecord(name string, res runner.Result) store.Run {
	return store.Run{
		JobName:         name,
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
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeRunSummary(w io.Writer, name string, res runner.Result) {
	status := "ok"
	if !res.Success() {
		status = "failed"
	}
	fmt.Fprintf(w, "job %s: %s\n", name, status)
	fmt.Fprintf(w, "  exit code: %d\n", res.ExitCode)
	fmt.Fprintf(w, "  duration:  %s\n", res.Duration.Round(time.Millisecond))
	fmt.Fprintf(w, "  timed out: %t\n", res.TimedOut)
	if res.Err != nil {
		fmt.Fprintf(w, "  error:     %s\n", res.Err)
	}
	writeStream(w, "stdout", res.Stdout, res.StdoutTruncated)
	writeStream(w, "stderr", res.Stderr, res.StderrTruncated)
}

func writeStream(w io.Writer, label string, data []byte, truncated bool) {
	if len(data) == 0 {
		fmt.Fprintf(w, "  %s: (empty)\n", label)
		return
	}
	note := ""
	if truncated {
		note = ", truncated"
	}
	fmt.Fprintf(w, "  %s (%d bytes%s):\n", label, len(data), note)
	preview := data
	if len(preview) > previewLimit {
		preview = preview[:previewLimit]
	}
	w.Write(preview)
	if len(preview) > 0 && preview[len(preview)-1] != '\n' {
		fmt.Fprintln(w)
	}
}
