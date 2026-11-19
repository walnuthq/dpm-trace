package integration

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const litShutdownGrace = 15 * time.Second

var registerSignals = signal.Notify

func cancelOnInterrupt() (ctx context.Context, interruptedCode func() int, brokenPipe func(), release func()) {
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	// Workaround for os/signal in the Go standard library: once SIGPIPE is
	// registered, Go delivers it for a write to a broken pipe on any descriptor,
	// sockets included, so a bare SIGPIPE is ignored and only a failed write to
	// the runner's own output counts as an interrupt.
	registerSignals(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGPIPE)
	done := make(chan struct{})
	var received atomic.Int32
	interrupt := func(number syscall.Signal) {
		received.CompareAndSwap(0, int32(number))
		cancel()
	}
	go func() {
		for {
			select {
			case sig := <-signals:
				if number, ok := sig.(syscall.Signal); ok && number != syscall.SIGPIPE {
					interrupt(number)
				}
			case <-done:
				return
			}
		}
	}()
	brokenPipe = func() { interrupt(syscall.SIGPIPE) }
	interruptedCode = func() int {
		if number := received.Load(); number != 0 {
			return 128 + int(number)
		}
		return 0
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			signal.Stop(signals)
			cancel()
			close(done)
		})
	}
	return ctx, interruptedCode, brokenPipe, release
}

type brokenPipeWatcher struct {
	io.Writer
	broken func()
}

func (w brokenPipeWatcher) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if errors.Is(err, syscall.EPIPE) {
		w.broken()
	}
	return n, err
}

func sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
