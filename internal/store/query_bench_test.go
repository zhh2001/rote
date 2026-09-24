package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// Keep both empty-output and output-heavy histories: metadata queries should
// not pay the cost of processing every historical stdout/stderr blob.
func BenchmarkLatestMetaPerJob(b *testing.B) {
	for _, size := range []int{0, 32 << 10} {
		b.Run(fmt.Sprintf("stream_bytes=%d", size), func(b *testing.B) {
			s, err := Open(filepath.Join(b.TempDir(), "rote.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer tx.Rollback()
			blob := make([]byte, size)
			for job := 0; job < 8; job++ {
				for i := 0; i < 128; i++ {
					r := makeRun(fmt.Sprintf("job-%d", job), base.Add(time.Duration(i)*time.Second))
					r.Stdout, r.Stderr = blob, blob
					if _, err := insertRun(ctx, tx, r); err != nil {
						b.Fatal(err)
					}
				}
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				latest, err := s.LatestMetaPerJob(ctx)
				if err != nil || len(latest) != 8 {
					b.Fatalf("latest count=%d: %v", len(latest), err)
				}
			}
		})
	}
}
