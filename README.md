# rote

**A cron that remembers what it did.**

[![CI](https://github.com/zhh2001/rote/actions/workflows/ci.yml/badge.svg)](https://github.com/zhh2001/rote/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/zhh2001/rote/branch/main/graph/badge.svg)](https://codecov.io/gh/zhh2001/rote)
[![Go Report Card](https://goreportcard.com/badge/github.com/zhh2001/rote)](https://goreportcard.com/report/github.com/zhh2001/rote)

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

```sh
go install github.com/zhh2001/rote/cmd/rote@latest
```

Or grab a prebuilt binary from the [Releases](https://github.com/zhh2001/rote/releases) page.

Homebrew (available once the first release is tagged):

```sh
brew install zhh2001/tap/rote
```

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

| Field        | Required | Description                                              |
|--------------|----------|----------------------------------------------------------|
| `name`       | yes      | Unique label for the job.                                |
| `schedule`   | yes      | When to run (see below).                                 |
| `command`    | yes      | Shell command, run via `sh -c`.                          |
| `timeout`    | no       | Max run time, e.g. `"30m"`, `"90s"`. Omit for no limit.  |
| `on_failure` | no       | Command run once when the job fails.                     |

Unknown keys are rejected, so a misspelled `timout` is caught instead of
silently ignored.

### Schedule syntax

Standard 5-field cron works:

```
*/15 * * * *      every 15 minutes
0 3 * * *         03:00 daily
0 9 * * 1         09:00 on Mondays
```

So do these plain-language forms:

```
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

| Command                       | What it does                                                        |
|-------------------------------|---------------------------------------------------------------------|
| `rote`                        | Schedule jobs and show the live dashboard together.                 |
| `rote start`                  | Run the scheduler headless, as a daemon.                            |
| `rote tui`                    | Read-only dashboard for an already-running scheduler.               |
| `rote run <job>`              | Run one job now, record it, and print a summary. Propagates the exit code (`124` on timeout, `127` if the job isn't found). |
| `rote list`                   | List jobs with their next and last run.                             |
| `rote logs <job> [-n N] [-o]` | Recent runs for a job; `-n` limits the count, `-o` includes the last run's output. |
| `rote version`                | Print the version.                                                  |

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

## Caveats

Don't run two schedulers against the same database. Running both `rote` and
`rote start` (or two daemons) pointed at the same `--db` will execute every job
twice. To watch a running scheduler, use the read-only `rote tui`.

## License

[MIT](LICENSE)
