package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zhh2001/rote/internal/config"
	"github.com/zhh2001/rote/internal/engine"
	"github.com/zhh2001/rote/internal/store"
	"github.com/zhh2001/rote/internal/tui"
)

const version = "0.0.1-dev"

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

// dispatch routes a subcommand and returns the process exit code.
func dispatch(args []string, stdout, stderr io.Writer) int {
	// No subcommand runs the all-in-one mode: schedule jobs and show the live
	// dashboard at once.
	if len(args) == 0 {
		return runIntegrated(nil, stdout, stderr)
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version":
		fmt.Fprintf(stdout, "rote %s\n", version)
		return 0
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	case "start":
		return runStart(rest, stdout, stderr)
	case "run":
		return runRun(rest, stdout, stderr)
	case "list":
		return runList(rest, stdout, stderr)
	case "logs":
		return runLogs(rest, stdout, stderr)
	case "tui":
		return runTUI(rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "rote: unknown command %q\n\n", cmd)
		printUsage(stderr)
		return 2
	}
}

// runTUI is the read-only dashboard: it loads config, opens the store, and shows
// the live view without scheduling anything.
func runTUI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfgFlag, dbFlag string
	addConfigFlag(fs, &cfgFlag)
	addDBFlag(fs, &dbFlag)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	jobs, st, code := loadConfigAndStore(cfgFlag, dbFlag, stderr)
	if code != 0 {
		return code
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := tui.Run(ctx, jobs, st); err != nil {
		fmt.Fprintf(stderr, "rote: %v\n", err)
		return 1
	}
	return 0
}

// runIntegrated schedules jobs (engine) while showing the live dashboard (TUI).
// When the TUI exits for any reason, the engine's context is canceled and we
// wait for it to finish before returning.
//
// TODO: add a lock to prevent a second scheduler (this mode or `rote start`)
// from running against the same database and executing every job twice.
func runIntegrated(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rote", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfgFlag, dbFlag string
	addConfigFlag(fs, &cfgFlag)
	addDBFlag(fs, &dbFlag)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	jobs, st, code := loadConfigAndStore(cfgFlag, dbFlag, stderr)
	if code != 0 {
		return code
	}
	defer st.Close()

	// Engine logs are discarded so they do not corrupt the alt-screen UI.
	eng, err := engine.New(jobs, st, nil)
	if err != nil {
		fmt.Fprintf(stderr, "rote: %v\n", err)
		return 1
	}

	engineCtx, cancelEngine := context.WithCancel(context.Background())
	engineDone := make(chan error, 1)
	go func() { engineDone <- eng.Run(engineCtx) }()

	tuiCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tuiErr := tui.Run(tuiCtx, jobs, st)

	cancelEngine()
	<-engineDone

	if tuiErr != nil {
		fmt.Fprintf(stderr, "rote: %v\n", tuiErr)
		return 1
	}
	return 0
}

func runStart(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfgFlag, dbFlag string
	addConfigFlag(fs, &cfgFlag)
	addDBFlag(fs, &dbFlag)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	jobs, st, code := loadConfigAndStore(cfgFlag, dbFlag, stderr)
	if code != 0 {
		return code
	}
	defer st.Close()

	// TODO: a second signal should force-exit; for now one signal triggers a
	// single graceful shutdown that waits for in-flight runs.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewTextHandler(stderr, nil))
	if err := cmdStart(ctx, logger, jobs, st); err != nil {
		fmt.Fprintf(stderr, "rote: %v\n", err)
		return 1
	}
	return 0
}

func runRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfgFlag, dbFlag string
	addConfigFlag(fs, &cfgFlag)
	addDBFlag(fs, &dbFlag)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "rote run: missing job name")
		return 2
	}

	jobs, st, code := loadConfigAndStore(cfgFlag, dbFlag, stderr)
	if code != 0 {
		return code
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cmdRun(ctx, stdout, jobs, st, fs.Arg(0)); err != nil {
		fmt.Fprintf(stderr, "rote: %v\n", err)
		return 1
	}
	return 0
}

func runList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfgFlag, dbFlag string
	addConfigFlag(fs, &cfgFlag)
	addDBFlag(fs, &dbFlag)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	jobs, st, code := loadConfigAndStore(cfgFlag, dbFlag, stderr)
	if code != 0 {
		return code
	}
	defer st.Close()

	if err := cmdList(context.Background(), stdout, jobs, st, time.Now()); err != nil {
		fmt.Fprintf(stderr, "rote: %v\n", err)
		return 1
	}
	return 0
}

func runLogs(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var dbFlag string
	var n int
	var output bool
	addDBFlag(fs, &dbFlag)
	fs.IntVar(&n, "n", 20, "number of runs to show")
	fs.BoolVar(&output, "output", false, "include the last run's stdout/stderr")
	fs.BoolVar(&output, "o", false, "include the last run's stdout/stderr (shorthand)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "rote logs: missing job name")
		return 2
	}

	dbResolved, err := resolveDBPath(dbFlag)
	if err != nil {
		fmt.Fprintf(stderr, "rote: %v\n", err)
		return 1
	}
	st, err := store.Open(dbResolved)
	if err != nil {
		fmt.Fprintf(stderr, "rote: %v\n", err)
		return 1
	}
	defer st.Close()

	if err := cmdLogs(context.Background(), stdout, st, fs.Arg(0), n, output); err != nil {
		fmt.Fprintf(stderr, "rote: %v\n", err)
		return 1
	}
	return 0
}

// loadConfigAndStore resolves paths, loads the config, and opens the store. On
// failure it writes a user-facing error to stderr and returns a non-zero code.
func loadConfigAndStore(cfgFlag, dbFlag string, stderr io.Writer) ([]config.Job, *store.Store, int) {
	cfgResolved, err := resolveConfigPath(cfgFlag)
	if err != nil {
		fmt.Fprintf(stderr, "rote: %v\n", err)
		return nil, nil, 1
	}
	jobs, err := config.Load(cfgResolved)
	if err != nil {
		fmt.Fprintf(stderr, "rote: %v\n", err)
		return nil, nil, 1
	}

	dbResolved, err := resolveDBPath(dbFlag)
	if err != nil {
		fmt.Fprintf(stderr, "rote: %v\n", err)
		return nil, nil, 1
	}
	st, err := store.Open(dbResolved)
	if err != nil {
		fmt.Fprintf(stderr, "rote: %v\n", err)
		return nil, nil, 1
	}
	return jobs, st, 0
}

func addConfigFlag(fs *flag.FlagSet, p *string) {
	fs.StringVar(p, "config", "", "path to jobs.toml")
	fs.StringVar(p, "c", "", "path to jobs.toml (shorthand)")
}

func addDBFlag(fs *flag.FlagSet, p *string) {
	fs.StringVar(p, "db", "", "path to the run-history database")
}

// resolveConfigPath applies the override or the OS default config location.
func resolveConfigPath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config directory: %w", err)
	}
	return configPath("", base), nil
}

// resolveDBPath applies the override or the XDG/home default database location.
func resolveDBPath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return dbPath("", os.Getenv("XDG_STATE_HOME"), home), nil
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `rote - a cron that remembers what it did

Usage:
  rote                 schedule jobs and show the live dashboard (all-in-one)
  rote <command> [flags] [args]

Commands:
  (no command)       run the scheduler and the dashboard together
  tui                read-only dashboard for an already-running scheduler
  start              run the scheduler in the foreground until interrupted
  run <job>          run a single job once and record the result
  list               list jobs with their next and last run
  logs <job>         show recent runs for a job
  version            print the version

Flags (rote, tui, start, run, list):
  -c, --config PATH  config file (default: <user-config-dir>/rote/jobs.toml)
      --db PATH      database file (default: <state-dir>/rote/rote.db)

logs flags:
      --db PATH      database file (as above)
  -n N               number of runs to show (default 20)
  -o, --output       also print the last run's stdout/stderr

Note: do not run a scheduler twice against the same database. Running both
'rote' and 'rote start' on the same db executes every job twice. To watch a
running scheduler, use 'rote tui'.
`)
}
