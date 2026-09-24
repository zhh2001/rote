package store

import (
	"context"
	"fmt"
)

// InsertWithRetention atomically stores a run and retains the newest keep runs
// for its job, ordered by start time then id (the same order as RecentRuns).
// Unlike Prune, keep == 0 disables deletion. Negative values are rejected.
// On any error, neither the insertion nor the deletion is committed.
// A late-finishing run older than the retained window may itself be pruned.
func (s *Store) InsertWithRetention(ctx context.Context, r Run, keep int) (int64, error) {
	if keep < 0 {
		return 0, fmt.Errorf("store: history limit must be non-negative")
	}
	if keep == 0 {
		return s.Insert(ctx, r)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin retention transaction: %w", err)
	}
	defer tx.Rollback()
	id, err := insertRun(ctx, tx, r)
	if err != nil {
		return 0, err
	}
	if err := pruneRuns(ctx, tx, r.JobName, keep); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit retention transaction: %w", err)
	}
	return id, nil
}
