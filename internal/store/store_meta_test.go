package store

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openMetaStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "rote.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// sameMeta asserts that a RunMeta matches the non-blob fields of a Run.
func sameMeta(t *testing.T, label string, m RunMeta, r Run) {
	t.Helper()
	if m.ID != r.ID || m.JobName != r.JobName || !m.StartedAt.Equal(r.StartedAt) ||
		!m.FinishedAt.Equal(r.FinishedAt) || m.Duration != r.Duration || m.ExitCode != r.ExitCode ||
		m.TimedOut != r.TimedOut || m.Success != r.Success ||
		m.StdoutTruncated != r.StdoutTruncated || m.StderrTruncated != r.StderrTruncated ||
		m.Err != r.Err {
		t.Errorf("%s: meta %+v does not match run %+v", label, m, r)
	}
}

// 1 & 3. Meta queries agree with the blob-loading methods on metadata, order,
// limit, and latest-per-job grouping.
func TestMetaMatchesFullQueries(t *testing.T) {
	s := openMetaStore(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)

	// Two jobs, several runs each, with varied flags and output.
	specs := []Run{
		{JobName: "a", ExitCode: 0, Success: true, Stdout: []byte("a0-out"), Stderr: nil, StdoutTruncated: false},
		{JobName: "a", ExitCode: 2, Success: false, Stdout: []byte("a1-out"), Stderr: []byte("a1-err"), StderrTruncated: true},
		{JobName: "a", ExitCode: -1, TimedOut: true, Success: false, Stdout: []byte("a2"), StdoutTruncated: true, Err: "boom"},
		{JobName: "b", ExitCode: 0, Success: true, Stdout: []byte("b0")},
	}
	for i := range specs {
		specs[i].StartedAt = base.Add(time.Duration(i) * time.Minute)
		specs[i].FinishedAt = specs[i].StartedAt.Add(time.Duration(i+1) * 250 * time.Millisecond)
		specs[i].Duration = time.Duration(i+1) * 250 * time.Millisecond
		if _, err := s.Insert(ctx, specs[i]); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}

	// RecentRunsMeta vs RecentRuns for job "a".
	full, err := s.RecentRuns(ctx, "a", 10)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	meta, err := s.RecentRunsMeta(ctx, "a", 10)
	if err != nil {
		t.Fatalf("RecentRunsMeta: %v", err)
	}
	if len(meta) != len(full) {
		t.Fatalf("RecentRunsMeta len = %d, want %d", len(meta), len(full))
	}
	for i := range full {
		sameMeta(t, "recent", meta[i], full[i])
	}

	// limit is honored identically.
	if got := mustRecentMeta(t, s, "a", 2); len(got) != 2 {
		t.Errorf("RecentRunsMeta limit: len = %d, want 2", len(got))
	}

	// LatestMetaPerJob vs LatestPerJob.
	fullLatest, err := s.LatestPerJob(ctx)
	if err != nil {
		t.Fatalf("LatestPerJob: %v", err)
	}
	metaLatest, err := s.LatestMetaPerJob(ctx)
	if err != nil {
		t.Fatalf("LatestMetaPerJob: %v", err)
	}
	if len(metaLatest) != len(fullLatest) {
		t.Fatalf("LatestMetaPerJob len = %d, want %d", len(metaLatest), len(fullLatest))
	}
	for job, r := range fullLatest {
		m, ok := metaLatest[job]
		if !ok {
			t.Errorf("LatestMetaPerJob missing job %q", job)
			continue
		}
		sameMeta(t, "latest "+job, m, r)
	}
}

func mustRecentMeta(t *testing.T, s *Store, job string, limit int) []RunMeta {
	t.Helper()
	m, err := s.RecentRunsMeta(context.Background(), job, limit)
	if err != nil {
		t.Fatalf("RecentRunsMeta: %v", err)
	}
	return m
}

// 2. RunOutput returns blobs and truncation flags byte-for-byte; unknown id -> ok=false.
func TestRunOutput(t *testing.T) {
	s := openMetaStore(t)
	ctx := context.Background()

	want := Run{
		JobName:         "job",
		StartedAt:       time.Now(),
		FinishedAt:      time.Now(),
		Stdout:          []byte{0x00, 0x01, 0xff, 'h', 'i', 0x00},
		Stderr:          []byte{0xde, 0xad, 0xbe, 0xef},
		StdoutTruncated: true,
		StderrTruncated: false,
	}
	id, err := s.Insert(ctx, want)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	out, ok, err := s.RunOutput(ctx, id)
	if err != nil {
		t.Fatalf("RunOutput: %v", err)
	}
	if !ok {
		t.Fatalf("RunOutput ok = false, want true")
	}
	if !bytes.Equal(out.Stdout, want.Stdout) {
		t.Errorf("Stdout = %v, want %v", out.Stdout, want.Stdout)
	}
	if !bytes.Equal(out.Stderr, want.Stderr) {
		t.Errorf("Stderr = %v, want %v", out.Stderr, want.Stderr)
	}
	if out.StdoutTruncated != want.StdoutTruncated || out.StderrTruncated != want.StderrTruncated {
		t.Errorf("truncation = (%v,%v), want (%v,%v)",
			out.StdoutTruncated, out.StderrTruncated, want.StdoutTruncated, want.StderrTruncated)
	}

	// Unknown id.
	if _, ok, err := s.RunOutput(ctx, id+9999); err != nil {
		t.Fatalf("RunOutput(missing): %v", err)
	} else if ok {
		t.Errorf("RunOutput(missing) ok = true, want false")
	}
}
