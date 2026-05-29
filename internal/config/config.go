// Package config loads and validates the jobs.toml job definitions.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/zhh2001/rote/internal/scheduler"
)

// Job is a validated job definition.
type Job struct {
	Name      string
	Schedule  string
	Command   string
	Timeout   time.Duration // parsed from the TOML string; 0 means no limit
	OnFailure string        // optional
}

// rawConfig mirrors the on-disk TOML structure before validation.
type rawConfig struct {
	Jobs []rawJob `toml:"job"`
}

type rawJob struct {
	Name      string `toml:"name"`
	Schedule  string `toml:"schedule"`
	Command   string `toml:"command"`
	Timeout   string `toml:"timeout"`
	OnFailure string `toml:"on_failure"`
}

// Load reads, parses, and validates the jobs.toml file at path. It returns all
// validation problems at once (joined) rather than stopping at the first, so a
// user can fix everything in one pass.
func Load(path string) ([]Job, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}

	var raw rawConfig
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}

	if len(raw.Jobs) == 0 {
		return nil, fmt.Errorf("config: %q defines no jobs", path)
	}

	var errs []error
	seen := make(map[string]bool)
	jobs := make([]Job, 0, len(raw.Jobs))

	for i, rj := range raw.Jobs {
		label := fmt.Sprintf("job #%d", i+1)
		if rj.Name != "" {
			label = fmt.Sprintf("job #%d (%q)", i+1, rj.Name)
		}

		job := Job{
			Name:      rj.Name,
			Schedule:  rj.Schedule,
			Command:   rj.Command,
			OnFailure: rj.OnFailure,
		}

		switch {
		case rj.Name == "":
			errs = append(errs, fmt.Errorf("%s: name must not be empty", label))
		case seen[rj.Name]:
			errs = append(errs, fmt.Errorf("%s: duplicate name %q", label, rj.Name))
		default:
			seen[rj.Name] = true
		}

		if rj.Command == "" {
			errs = append(errs, fmt.Errorf("%s: command must not be empty", label))
		}

		if _, err := scheduler.Parse(rj.Schedule); err != nil {
			errs = append(errs, fmt.Errorf("%s: invalid schedule %q: %w", label, rj.Schedule, err))
		}

		if rj.Timeout != "" {
			d, err := time.ParseDuration(rj.Timeout)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: invalid timeout %q: %w", label, rj.Timeout, err))
			} else {
				job.Timeout = d
			}
		}

		jobs = append(jobs, job)
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return jobs, nil
}
