package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
)

var errStoreClosed = errors.New("store is closed")

type writeQueue struct {
	gate  chan struct{}
	users int // protected by writeQueuesMu
}

var (
	writeQueuesMu sync.Mutex
	writeQueues   = make(map[string]*writeQueue)
)

// acquireWriteQueue shares admission across Stores for the same database in
// this process. Ask SQLite for its filename so relative paths and file: URIs
// agree, then resolve symlinks just as the scheduler lock does.
func acquireWriteQueue(db *sql.DB) (chan struct{}, func(), error) {
	var seq int
	var name, path string
	if err := db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		return nil, nil, fmt.Errorf("store: locate writer database: %w", err)
	}
	if path == "" {
		// Private in-memory databases do not share a file or a write lock.
		return make(chan struct{}, 1), func() {}, nil
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, nil, fmt.Errorf("store: resolve writer database: %w", err)
	}
	writeQueuesMu.Lock()
	queue := writeQueues[path]
	if queue == nil {
		queue = &writeQueue{gate: make(chan struct{}, 1)}
		writeQueues[path] = queue
	}
	queue.users++
	writeQueuesMu.Unlock()
	return queue.gate, func() {
		writeQueuesMu.Lock()
		defer writeQueuesMu.Unlock()
		queue.users--
		if queue.users == 0 {
			delete(writeQueues, path)
		}
	}, nil
}

// lockWrite admits one write operation (including its entire transaction) per
// database in this process. SQLite has only one writer at a time; making every
// goroutine contend through its own connection wastes the busy timeout locally.
// Waiting here is cancelable and does not occupy a database connection. Reads
// use the pool directly, while other processes still use SQLite's locks.
func (s *Store) lockWrite(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-s.closed:
		return errStoreClosed
	default:
	}
	select {
	case s.writes <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closed:
		return errStoreClosed
	}
	// Admission can race with cancellation or Close. Recheck before accessing
	// SQLite so canceled/closed waiters cannot mutate history after waking.
	if err := ctx.Err(); err != nil {
		s.unlockWrite()
		return err
	}
	select {
	case <-s.closed:
		s.unlockWrite()
		return errStoreClosed
	default:
		return nil
	}
}

func (s *Store) unlockWrite() { <-s.writes }
