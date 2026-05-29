package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// 1. A successful command: exit 0, captured stdout, positive duration.
func TestRunSuccess(t *testing.T) {
	res := Run(context.Background(), Spec{Name: "ok", Command: "echo hello"})
	if !res.Success() {
		t.Fatalf("Success() = false, want true (res=%+v)", res)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if res.Err != nil {
		t.Errorf("Err = %v, want nil", res.Err)
	}
	if res.Name != "ok" {
		t.Errorf("Name = %q, want %q", res.Name, "ok")
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "hello" {
		t.Errorf("Stdout = %q, want %q", got, "hello")
	}
	if res.Duration <= 0 {
		t.Errorf("Duration = %v, want > 0", res.Duration)
	}
	if res.FinishedAt.Before(res.StartedAt) {
		t.Errorf("FinishedAt %v before StartedAt %v", res.FinishedAt, res.StartedAt)
	}
}

// 2. A non-zero exit is reported via ExitCode, not Err.
func TestRunNonZeroExit(t *testing.T) {
	res := Run(context.Background(), Spec{Command: "exit 3"})
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
	if res.Success() {
		t.Errorf("Success() = true, want false")
	}
	if res.Err != nil {
		t.Errorf("Err = %v, want nil (non-zero exit is not a runner error)", res.Err)
	}
	if res.TimedOut {
		t.Errorf("TimedOut = true, want false")
	}
}

// 3. stdout and stderr are captured separately and do not bleed into each other.
func TestRunStreamSeparation(t *testing.T) {
	res := Run(context.Background(), Spec{Command: "echo out; echo err 1>&2"})
	out := strings.TrimSpace(string(res.Stdout))
	errOut := strings.TrimSpace(string(res.Stderr))
	if out != "out" {
		t.Errorf("Stdout = %q, want %q", out, "out")
	}
	if errOut != "err" {
		t.Errorf("Stderr = %q, want %q", errOut, "err")
	}
	if strings.Contains(out, "err") {
		t.Errorf("Stdout %q unexpectedly contains stderr output", out)
	}
	if strings.Contains(errOut, "out") {
		t.Errorf("Stderr %q unexpectedly contains stdout output", errOut)
	}
}

// 4. A timeout terminates the command and returns well before its sleep ends.
func TestRunTimeout(t *testing.T) {
	start := time.Now()
	res := Run(context.Background(), Spec{Command: "sleep 10", Timeout: 100 * time.Millisecond})
	elapsed := time.Since(start)

	if !res.TimedOut {
		t.Errorf("TimedOut = false, want true")
	}
	if res.Success() {
		t.Errorf("Success() = true, want false")
	}
	if elapsed > 2*time.Second {
		t.Errorf("Run took %v, want < 2s", elapsed)
	}
}

// 5. Process-group kill: a backgrounded descendant is terminated rather than
// orphaned, and Run returns promptly without blocking on the held pipe.
func TestRunProcessGroupKill(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	// The shell exits immediately after echo, leaving the subshell backgrounded;
	// only a whole-group kill prevents it from creating the marker after 1s.
	command := fmt.Sprintf("(sleep 1; touch %q) & echo started", marker)

	start := time.Now()
	res := Run(context.Background(), Spec{Command: command, Timeout: 200 * time.Millisecond})
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("Run blocked for %v, want prompt return", elapsed)
	}
	if !strings.Contains(string(res.Stdout), "started") {
		t.Errorf("Stdout = %q, want to contain %q", res.Stdout, "started")
	}

	// Wait past the descendant's 1s sleep; the marker must never appear.
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("backgrounded descendant survived process-group termination (marker exists)")
	}
}

// 6. Output beyond the cap is truncated without hanging or unbounded growth.
func TestRunOutputCap(t *testing.T) {
	size := 3 * 1024 * 1024 // well over the 1 MiB cap
	command := fmt.Sprintf("head -c %d /dev/zero; head -c %d /dev/zero 1>&2", size, size)

	res := Run(context.Background(), Spec{Command: command, Timeout: 10 * time.Second})

	if res.TimedOut {
		t.Fatalf("TimedOut = true, want false (command should finish quickly)")
	}
	if !res.StdoutTruncated {
		t.Errorf("StdoutTruncated = false, want true")
	}
	if !res.StderrTruncated {
		t.Errorf("StderrTruncated = false, want true")
	}
	if len(res.Stdout) != maxOutputBytes {
		t.Errorf("len(Stdout) = %d, want %d", len(res.Stdout), maxOutputBytes)
	}
	if len(res.Stderr) != maxOutputBytes {
		t.Errorf("len(Stderr) = %d, want %d", len(res.Stderr), maxOutputBytes)
	}
}

// 7. A command that cannot be started surfaces an Err and ExitCode -1.
func TestRunStartFailure(t *testing.T) {
	// An empty PATH makes the "sh" lookup fail, so the process never starts.
	t.Setenv("PATH", "")
	res := Run(context.Background(), Spec{Command: "echo hi"})

	if res.Err == nil {
		t.Errorf("Err = nil, want a start error")
	}
	if res.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", res.ExitCode)
	}
	if res.Success() {
		t.Errorf("Success() = true, want false")
	}
}

// 8. External cancellation kills the process but is distinguished from a timeout.
func TestRunExternalCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	res := Run(ctx, Spec{Command: "sleep 10"})
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("Run took %v, want < 2s", elapsed)
	}
	if res.TimedOut {
		t.Errorf("TimedOut = true, want false for external cancellation")
	}
	if res.Success() {
		t.Errorf("Success() = true, want false")
	}
}

// 9. Concurrent runs of distinct commands do not cross-contaminate results.
func TestRunConcurrent(t *testing.T) {
	const n = 24
	results := make([]Result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = Run(context.Background(), Spec{
				Name:    "job-" + strconv.Itoa(i),
				Command: "echo " + strconv.Itoa(i*7),
			})
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		r := results[i]
		if !r.Success() {
			t.Errorf("job %d: Success() = false (res=%+v)", i, r)
		}
		if want := "job-" + strconv.Itoa(i); r.Name != want {
			t.Errorf("job %d: Name = %q, want %q", i, r.Name, want)
		}
		if want, got := strconv.Itoa(i*7), strings.TrimSpace(string(r.Stdout)); got != want {
			t.Errorf("job %d: Stdout = %q, want %q", i, got, want)
		}
	}
}
