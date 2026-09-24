package main

import (
	"context"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestSchedulerShutdownStages(t *testing.T) {
	for _, tc := range []struct {
		name string
		quit bool
		sig  os.Signal
		code int
	}{
		{"interrupt", false, os.Interrupt, 130},
		{"terminate", false, syscall.SIGTERM, 143},
		{"quit then interrupt", true, os.Interrupt, 130},
		{"quit then terminate", true, syscall.SIGTERM, 143},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			notified := make(chan bool, 4)
			s := newSchedulerShutdown(context.Background(), func() { calls.Add(1) }, func(forced bool) { notified <- forced })
			defer s.Close()
			if tc.quit {
				if !s.begin() || s.begin() {
					t.Fatal("begin must request graceful shutdown exactly once")
				}
			} else {
				s.signals <- syscall.SIGTERM
				select {
				case forced := <-notified:
					if forced {
						t.Fatal("first signal forced shutdown")
					}
				case <-time.After(time.Second):
					t.Fatal("first signal was not handled")
				}
			}
			if s.ctx.Err() == nil || calls.Load() != 0 || s.exitCode() != 0 {
				t.Fatal("graceful shutdown canceled running jobs or failed to stop scheduling")
			}
			s.signals <- tc.sig
			select {
			case forced := <-notified:
				if !forced {
					t.Fatal("second request did not force shutdown")
				}
			case <-time.After(time.Second):
				t.Fatal("force signal was not handled")
			}
			s.signals <- os.Interrupt
			s.Close()
			s.Close()
			if calls.Load() != 1 || s.exitCode() != tc.code {
				t.Fatalf("force calls=%d code=%d, want 1/%d", calls.Load(), s.exitCode(), tc.code)
			}
		})
	}
}

func TestSchedulerShutdownCloseWithoutSignal(t *testing.T) {
	s := newSchedulerShutdown(context.Background(), func() { t.Error("unexpected force stop") }, nil)
	s.Close()
	if s.ctx.Err() == nil || s.exitCode() != 0 {
		t.Fatal("Close must release context without forcing shutdown")
	}
}
