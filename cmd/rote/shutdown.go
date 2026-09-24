package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
)

// schedulerShutdown keeps signal handling alive while the engine drains. The
// first request stops scheduling; subsequent signals cancel work without
// bypassing deferred cleanup or the persistence of canceled runs.
type schedulerShutdown struct {
	ctx     context.Context
	cancel  context.CancelFunc
	signals chan os.Signal
	done    chan struct{}
	stopped chan struct{}
	close   sync.Once
	mu      sync.Mutex // serializes a TUI quit with an arriving signal
	code    atomic.Int32
}

func newSchedulerShutdown(parent context.Context, force func(), notify func(bool)) *schedulerShutdown {
	ctx, cancel := context.WithCancel(parent)
	s := &schedulerShutdown{
		ctx: ctx, cancel: cancel,
		signals: make(chan os.Signal, 2),
		done:    make(chan struct{}), stopped: make(chan struct{}),
	}
	signal.Notify(s.signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		defer close(s.stopped)
		for {
			select {
			case <-s.done:
				return
			case sig := <-s.signals:
				if s.begin() {
					if notify != nil {
						notify(false)
					}
					continue
				}
				code := int32(128 + int(syscall.SIGTERM))
				if sig == os.Interrupt {
					code = 130
				}
				if s.code.CompareAndSwap(0, code) {
					force()
					if notify != nil {
						notify(true)
					}
				}
			}
		}
	}()
	return s
}

// begin is also used when the TUI quits (q/Ctrl+C) or fails. A subsequent
// signal then requests force cancellation, rather than another graceful stop.
func (s *schedulerShutdown) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return false
	}
	s.cancel()
	return true
}

func (s *schedulerShutdown) Close() {
	s.close.Do(func() {
		signal.Stop(s.signals)
		close(s.done)
		<-s.stopped
		s.cancel()
	})
}

func (s *schedulerShutdown) exitCode() int {
	return int(s.code.Load())
}
