# rote

**A cron that remembers what it did.**

[![CI](https://github.com/zhh2001/rote/actions/workflows/ci.yml/badge.svg)](https://github.com/zhh2001/rote/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/zhh2001/rote/branch/main/graph/badge.svg)](https://codecov.io/gh/zhh2001/rote)

![rote demo](docs/demo.gif)

## Why

`cron` runs your jobs and forgets them. When a backup silently stops firing or a
script starts exiting non-zero at 3am, there's nothing to look at — no exit
code, no timing, no output, often no sign it ran at all.

rote runs jobs on a schedule and records every run: exit code, duration, whether
it timed out, and the captured stdout/stderr. A terminal dashboard shows, at a
glance, which jobs are healthy, when each runs next, and what the last failure
actually printed.

## Install

Install script (Linux and macOS) — downloads the right binary for your platform:

```sh
curl -fsSL https://raw.githubusercontent.com/zhh2001/rote/main/install.sh | sh
```

With Go:

```sh
go install github.com/zhh2001/rote/cmd/rote@latest
```

Homebrew (available once the first release is tagged):

```sh
brew install zhh2001/tap/rote
```

Linux packages — download the `.deb`/`.rpm`/`.apk` for your architecture from the [Releases](https://github.com/zhh2001/rote/releases) page, then:

```sh
sudo dpkg -i rote_*.deb          # Debian/Ubuntu
sudo rpm -i rote_*.rpm           # Fedora/RHEL/openSUSE
apk add --allow-untrusted rote_*.apk   # Alpine
```

Or grab a prebuilt binary archive from the same Releases page.

## Quick start

Drop a config at `~/.config/rote/jobs.toml`:

```toml
[[job]]
name = "heartbeat"
schedule = "every 5m"
command = "curl -fsS https://example.com/health"

[[job]]
name = "nightly-backup"
schedule = "daily at 03:00"
command = "/usr/local/bin/backup.sh"
timeout = "30m"
on_failure = "notify-send 'backup failed'"
```

Then run the scheduler with the live dashboard:

```sh
rote
```

Or run it headless as a daemon (no UI):

```sh
rote start
```

## Configuration

Jobs live in a TOML file as an array of `[[job]]` tables:

| Field           | Required | Description                                                                                     |
| --------------- | -------- | ----------------------------------------------------------------------------------------------- |
| `name`          | yes      | Unique label for the job.                                                                       |
| `schedule`      | yes      | When to run (see below).                                                                        |
| `command`       | yes      | Shell command, run via `sh -c`.                                                                 |
| `timeout`       | no       | Non-negative max run time, e.g. `"30m"`, `"90s"`. Omit or use `"0s"` for no limit.              |
| `on_failure`    | no       | Command run once when the job fails.                                                            |
| `history_limit` | no       | Non-negative integer: keep the newest N runs for this job. Omit or use `0` to keep all history. |

Unknown keys are rejected, so a misspelled `timout` is caught instead of
silently ignored.

### History retention

History is unlimited by default. To opt in to automatic cleanup, add, for
example, `history_limit = 1000` inside a job's `[[job]]` table. After each run,
scheduled or manual, rote stores its result and removes that job's older records
in one transaction. If either operation fails, the transaction is rolled back
and existing history is left unchanged. Other jobs are unaffected.

The limit counts all results, including failures, timeouts, and cancellations.
"Newest" means latest start time, with the record ID breaking ties, matching the
history display. A long-running job that finishes after newer runs can therefore
fall outside the retained window. Manual runs and the scheduler should use the
same configuration to apply the same limit.

Cleanup first takes effect when that job next records a run; merely starting a viewer, running `list`/`logs`, or loading configuration never prunes history. Removing the setting or setting it to `0` stops future cleanup but cannot restore deleted records or their captured output. Back up the database before enabling or lowering a limit if you need to preserve that history.

Freed SQLite pages can be reused by later writes; the database file does not necessarily shrink immediately. No automatic `VACUUM` is performed. This is a record-count limit, not a database byte quota.

### Schedule syntax

Standard 5-field cron works:

```txt
*/15 * * * *      every 15 minutes
0 3 * * *         03:00 daily
0 9 * * 1         09:00 on Mondays
```

So do these plain-language forms:

```txt
every 5m          every 90s          every 1h30m
hourly            daily              weekly            monthly
daily at 03:00
every monday at 09:00
```

The smallest effective interval is about **1 second** — sub-second schedules are
rounded up.

### Files

- **Config**: your user config dir, i.e. `~/.config/rote/jobs.toml` (override with `-c`/`--config`).
- **Database**: your XDG state dir, i.e. `~/.local/state/rote/rote.db` (override with `--db`).

## Commands

| Command                       | What it does                                                                                                                                                                       |
| ----------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `rote`                        | Schedule jobs and show the live dashboard together.                                                                                                                                |
| `rote start`                  | Run the scheduler headless, as a daemon.                                                                                                                                           |
| `rote tui`                    | Read-only dashboard for an already-running scheduler.                                                                                                                              |
| `rote run <job>`              | Run one job now, record it, and print a summary. Propagates the command's exit code (`124` on timeout, `126` on runner error or signal termination, `127` if the job isn't found). |
| `rote list`                   | List jobs with their next and last run.                                                                                                                                            |
| `rote logs <job> [-n N] [-o]` | Recent runs for a job; `-n` limits the count, `-o` includes the last run's output.                                                                                                 |
| `rote version`                | Print the version.                                                                                                                                                                 |

The recorded and displayed exit code belongs to the shell process. If the shell exits successfully but output capture fails, the run is marked failed and `rote run` exits with `126`; the recorded shell exit code remains `0`.

Canceling a manual run with Ctrl+C or SIGTERM terminates its process group, records `context canceled` as a failure (not a timeout), and exits with `126`, including when the shell already exited but a descendant still held its output pipes open.

In the dashboard: `↑`/`↓` (or `k`/`j`) to move, `Enter` to open a job's history,
`Tab` to switch between the history list and the output pane, `Esc` to go back,
`r` to refresh, `?` for help, `q` to quit.

## Running as a service

A minimal systemd user unit:

```ini
[Unit]
Description=rote job scheduler
After=network-online.target

[Service]
ExecStart=%h/go/bin/rote start
Restart=on-failure

[Install]
WantedBy=default.target
```

Save it as `~/.config/systemd/user/rote.service`, then:

```sh
systemctl --user enable --now rote.service
```

Watch it live from another terminal with `rote tui`.

### Stopping the scheduler

The first Ctrl+C or SIGTERM stops scheduling new jobs and waits for running jobs and their failure hooks to finish. Quitting the integrated dashboard with `q` also starts this graceful shutdown. The database lock stays held until the work is finished and its results have been recorded.

If a job is stuck, press Ctrl+C again or send another SIGTERM. After quitting the dashboard, one such signal is enough. This cancels running jobs and hooks by terminating their process groups, records canceled jobs as failures rather than timeouts, and skips new failure hooks. An already-failed job keeps its original result if only its hook was canceled. The scheduler then closes the database, releases its lock, and exits with `130` for SIGINT or `143` for SIGTERM.

Ordinary graceful shutdown returns `0`. A job with no timeout can keep graceful shutdown waiting indefinitely; configure `timeout` or use the second signal to cancel it. Forced shutdown still waits for process cleanup and database writes; it does not bypass them with an immediate process exit.

## Caveats

Only one scheduler (`rote` or `rote start`) may run against a database at a time. A second scheduler exits with an error. To watch a running scheduler, use the read-only `rote tui`; `list`, `logs`, and manual `run` also remain available. Manual runs are independent and may overlap a scheduled run.

The scheduler holds an OS lock on `<database>.lock` beside the resolved database file, including through graceful shutdown while jobs finish. The lock is released automatically if the process exits or crashes. The empty lock file remains for reuse; do not delete it while a scheduler is running. Scheduling requires a file-backed database on Linux or macOS.

## License

[MIT](LICENSE)
