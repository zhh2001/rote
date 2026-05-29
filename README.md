# rote

A cron that remembers what it did.

## Install

```sh
# TODO: installation instructions
```

## Quick start

Copy the example config and define your jobs:

```sh
cp jobs.example.toml jobs.toml
```

List configured jobs:

```sh
rote list
```

Run a job by name:

```sh
rote run nightly-backup
```

## Why

Plain cron runs your jobs and forgets them. When something breaks at 3am, you
are left guessing what happened. rote keeps a record of every run — exit code,
duration, and output — and surfaces it in a terminal UI so you can see at a
glance which jobs are healthy and which need attention.

## License

[MIT](LICENSE)
