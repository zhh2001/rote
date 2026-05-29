package runner

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"
)

// maxOutputBytes caps how much of each output stream is retained. Output beyond
// this limit is discarded (the stream's Truncated flag is set) so that a noisy
// command cannot exhaust memory.
const maxOutputBytes = 1 << 20 // 1 MiB per stream

// waitDelay bounds how long Wait blocks after the process exits or is killed,
// guarding against orphaned children that keep the output pipes open.
const waitDelay = 5 * time.Second

// Spec describes a single command to execute.
type Spec struct {
	Name    string        // label applied to the result; not used for execution
	Command string        // command line executed via "sh -c"
	Timeout time.Duration // 0 means no time limit
}

// Result captures the outcome of running a Spec.
type Result struct {
	Name            string
	StartedAt       time.Time
	FinishedAt      time.Time
	Duration        time.Duration
	ExitCode        int // process exit code; -1 if it could not start or was killed
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
	TimedOut        bool
	Err             error // start/execution error, distinct from a non-zero exit
}

// Success reports whether the command ran to completion with a zero exit code.
func (r Result) Success() bool {
	return r.Err == nil && !r.TimedOut && r.ExitCode == 0
}

// Run executes spec's command through "sh -c" and captures its result. It keeps
// only local state and may be called concurrently from multiple goroutines.
//
// The command runs in its own process group so that, on timeout or context
// cancellation, the entire group is killed rather than just the direct child;
// this prevents orphaned descendants from leaking or from holding the output
// pipes open and blocking Wait indefinitely.
func Run(ctx context.Context, spec Spec) Result {
	if ctx == nil {
		ctx = context.Background()
	}

	started := time.Now()
	res := Result{Name: spec.Name, StartedAt: started, ExitCode: -1}

	runCtx := ctx
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	cmd := exec.Command("sh", "-c", spec.Command)
	configureProcessGroup(cmd)
	// WaitDelay bounds Wait even if a descendant keeps the output pipes open, so
	// Wait is guaranteed to return.
	cmd.WaitDelay = waitDelay

	stdout := &cappedBuffer{limit: maxOutputBytes}
	stderr := &cappedBuffer{limit: maxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	finish := func() Result {
		now := time.Now()
		res.FinishedAt = now
		res.Duration = now.Sub(started)
		res.Stdout = stdout.Bytes()
		res.Stderr = stderr.Bytes()
		res.StdoutTruncated = stdout.Truncated()
		res.StderrTruncated = stderr.Truncated()
		return res
	}

	if err := cmd.Start(); err != nil {
		res.Err = err
		return finish()
	}

	// Watchdog: terminate the whole process group when the context ends. This is
	// done here rather than via exec's own context handling because the direct
	// child may exit (e.g. after backgrounding a job) before the deadline, after
	// which exec stops watching the context and a lingering descendant could keep
	// the output pipes open until WaitDelay elapses.
	done := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			_ = terminate(cmd)
		case <-done:
		}
	}()

	waitErr := cmd.Wait()
	close(done)

	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	res.TimedOut = errors.Is(runCtx.Err(), context.DeadlineExceeded)

	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
		// Clean exit with code 0.
	case errors.As(waitErr, &exitErr):
		// The command ran and exited (non-zero or killed by signal); the exit
		// code already reflects this, so it is not a runner-level error.
	case runCtx.Err() != nil:
		// Abnormal result caused by our own context-driven termination; this is
		// reported via TimedOut and the exit code, not as a runner error.
	default:
		res.Err = waitErr
	}

	return finish()
}

// cappedBuffer is an io.Writer that retains at most limit bytes and records
// whether any input was dropped. Writes never short-write or error, so the
// process being captured is never blocked once the cap is reached.
type cappedBuffer struct {
	mu        sync.Mutex
	buf       []byte
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if room := b.limit - len(b.buf); room > 0 {
		if room >= len(p) {
			b.buf = append(b.buf, p...)
		} else {
			b.buf = append(b.buf, p[:room]...)
			b.truncated = true
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, len(b.buf))
	copy(out, b.buf)
	return out
}

func (b *cappedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}
