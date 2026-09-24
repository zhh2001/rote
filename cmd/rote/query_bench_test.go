package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhh2001/rote/internal/config"
	"github.com/zhh2001/rote/internal/store"
)

func BenchmarkReadCommands(b *testing.B) {
	for _, size := range []int{0, 64 << 10} {
		b.Run(fmt.Sprintf("stream_bytes=%d", size), func(b *testing.B) {
			st, err := store.Open(filepath.Join(b.TempDir(), "rote.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer st.Close()
			ctx := context.Background()
			now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
			blob := make([]byte, size)
			for i := 0; i < 20; i++ {
				if _, err := st.Insert(ctx, store.Run{
					JobName: "job", StartedAt: now.Add(time.Duration(i-20) * time.Minute),
					Success: true, Stdout: blob, Stderr: blob,
				}); err != nil {
					b.Fatal(err)
				}
			}
			jobs := []config.Job{{Name: "job", Schedule: "@hourly"}}
			for _, command := range []string{"list", "logs", "logs-output"} {
				b.Run(command, func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						var err error
						if command == "list" {
							err = cmdList(ctx, io.Discard, jobs, st, now)
						} else {
							err = cmdLogs(ctx, io.Discard, st, "job", 20, command == "logs-output")
						}
						if err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}
