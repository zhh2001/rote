# rote

**A cron that remembers what it did.**

[![CI](https://github.com/zhh2001/rote/actions/workflows/ci.yml/badge.svg)](https://github.com/zhh2001/rote/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/zhh2001/rote/branch/main/graph/badge.svg)](https://codecov.io/gh/zhh2001/rote)

![rote demo](docs/demo.gif)

## Why

`cron` runs your jobs and forgets them. When a backup silently stops firing or a script starts exiting non-zero at 3am, there's nothing to look at — no exit code, no timing, no output, often no sign it ran at all.

rote runs jobs on a schedule and records every run: exit code, duration, whether it timed out, and the captured stdout/stderr. A terminal dashboard shows, at a glance, which jobs are healthy, when each runs next, and what the last failure actually printed.

## Install

Install script (Linux and macOS) — downloads the right binary for your platform:

```sh
curl -fsSL https://raw.githubusercontent.com/zhh2001/rote/main/install.sh | sh
```

The installer requires `curl` or `wget` and `sha256sum` or `shasum`. SHA-256 verification is mandatory: missing tools, failed checksum downloads/calculations, invalid checksum entries, or checksum mismatches stop installation before extracting the archive or replacing an existing binary.

With Go:

```sh
go install github.com/zhh2001/rote/cmd/rote@latest
```

Homebrew (macOS):

```sh
brew install --cask zhh2001/tap/rote
```

Linux packages — download the `.deb`/`.rpm`/`.apk` for your architecture from the [Releases](https://github.com/zhh2001/rote/releases) page, then:

```sh
sudo dpkg -i rote_*.deb               # Debian/Ubuntu
sudo rpm -i rote_*.rpm                # Fedora/RHEL/openSUSE
apk add --allow-untrusted rote_*.apk  # Alpine
```

Or grab a prebuilt binary archive from the same Releases page.

### Compatibility and upgrades

Starting with v1.0.0, the documented CLI commands/flags, configuration fields, exit-code meanings, and the ability to read existing run history are the stable user-facing interface. Compatible additions and fixes stay within 1.x; incompatible changes to that interface require a new major version. Terminal layout and human-readable table formatting are not machine-readable APIs, and the raw SQLite schema is an implementation detail, not a supported SQL API.

When upgrading, stop schedulers and manual runs, allow graceful shutdown to finish, and back up your configuration and database before replacing the binary. v1.0.1 uses the same database schema as v0.2.1 and leaves history unlimited unless you explicitly configure `history_limit`. See the [v1.0.1 release notes](docs/releases/v1.0.1.md) for behavior changes and rollback cautions. Release maintainers can follow the [release checklist](docs/RELEASING.md).

The v1.0.0 source tag is retained, but its release workflow stopped before publishing binary assets. v1.0.1 is the replacement release target; the existing tag is not moved or reused.

## Quick start

Run `rote init` to create a starter config and print its location, then edit it. The default is `~/.config/rote/jobs.toml` on Linux (or under `$XDG_CONFIG_HOME` when set), and `~/Library/Application Support/rote/jobs.toml` on macOS. For example:

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

Unknown keys are rejected, so a misspelled `timout` is caught instead of silently ignored.

### History retention

History is unlimited by default. To opt in to automatic cleanup, add, for example, `history_limit = 1000` inside a job's `[[job]]` table. After each run, scheduled or manual, rote stores its result and removes that job's older records in one transaction. If either operation fails, the transaction is rolled back and existing history is left unchanged. Other jobs are unaffected.

The limit counts all results, including failures, timeouts, and cancellations. "Newest" means latest start time, with the record ID breaking ties, matching the history display. A long-running job that finishes after newer runs can therefore fall outside the retained window. Manual runs and the scheduler should use the same configuration to apply the same limit.

Cleanup first takes effect when that job next records a run; merely starting a viewer, running `list`/`logs`, or loading configuration never prunes history. Removing the setting or setting it to `0` stops future cleanup but cannot restore deleted records or their captured output. Back up the database before enabling or lowering a limit if you need to preserve that history.

Freed SQLite pages can be reused by later writes; the database file does not necessarily shrink immediately. No automatic `VACUUM` is performed. This is a record-count limit, not a database byte quota.

Within one process, handles for the same database share a write queue, including the entire insert-and-retain transaction, while history reads and unrelated databases remain independent. Relative paths, file URIs, and symlinks resolving to the same filename share that queue. A write waiting locally can be canceled without waiting for SQLite's busy timeout. Different processes still coordinate through SQLite; an external writer holding the database lock too long can still cause the existing five-second busy timeout. Writes are not retried automatically, and a persistence failure is reported rather than treated as a saved result.

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

The smallest effective interval is about **1 second** — sub-second schedules are rounded up.

### Files

- **Config (Linux)**: `$XDG_CONFIG_HOME/rote/jobs.toml` when set, otherwise `~/.config/rote/jobs.toml`.
- **Config (macOS)**: `~/Library/Application Support/rote/jobs.toml`; `XDG_CONFIG_HOME` does not affect this path.
- **Database (Linux and macOS)**: `$XDG_STATE_HOME/rote/rote.db` when set, otherwise `~/.local/state/rote/rote.db`.

Override the config path with `-c`/`--config` and the database path with `--db`.
Quote paths containing spaces, for example `rote list -c "$HOME/Library/Application Support/rote/jobs.toml"`.

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
| `rote init`                   | Create a starter config at the platform's default location (or `-c` path), without overwriting an existing file.                                                                   |

Lists and history tables read metadata only; `logs -o` loads captured output only for the newest displayed run. If that run is concurrently removed by history retention, it reports the output as unavailable instead of displaying another run's output.

The recorded and displayed exit code belongs to the shell process. If the shell exits successfully but output capture fails, the run is marked failed and `rote run` exits with `126`; the recorded shell exit code remains `0`.

Canceling a manual run with Ctrl+C or SIGTERM terminates its process group, records `context canceled` as a failure (not a timeout), and exits with `126`, including when the shell already exited but a descendant still held its output pipes open.

In the dashboard: `↑`/`↓` (or `k`/`j`) to move, `Enter` to open a job's history, `Tab` to switch between the history list and the output pane, `Esc` to go back, `r` to refresh, `?` for help, `q` to quit.

The dashboard retries failed reads on its next one-second refresh or when you press `r`. List/history errors appear alongside the last successfully loaded data, if any; output errors appear in the output pane. Each error clears when its read succeeds. Successfully loaded output is cached for the selected run.

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
