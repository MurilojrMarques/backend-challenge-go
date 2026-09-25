package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestLoopRunsUntilStopped(t *testing.T) {
	t.Parallel()

	var ticks atomic.Int32
	l := NewLoop("t", discard, time.Millisecond, func(ctx context.Context) (bool, error) {
		ticks.Add(1)
		return false, nil
	})
	require.NoError(t, l.Start(context.Background()))
	assert.Error(t, l.Start(context.Background()), "double start is refused")

	assert.Eventually(t, func() bool { return ticks.Load() >= 3 }, time.Second, time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, l.Stop(ctx))
	after := ticks.Load()
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, after, ticks.Load(), "no ticks after stop")
}

func TestLoopBusyAndErrorBackoff(t *testing.T) {
	t.Parallel()

	var ticks atomic.Int32
	busy := NewLoop("busy", discard, time.Hour, func(context.Context) (bool, error) {
		ticks.Add(1)
		return true, nil
	})
	require.NoError(t, busy.Start(context.Background()))
	assert.Eventually(t, func() bool { return ticks.Load() >= 5 }, time.Second, time.Millisecond, "busy ticks skip the interval")
	require.NoError(t, busy.Stop(context.Background()))

	var failures atomic.Int32
	failing := NewLoop("failing", discard, 5*time.Millisecond, func(context.Context) (bool, error) {
		failures.Add(1)
		return false, errors.New("boom")
	})
	require.NoError(t, failing.Start(context.Background()))
	time.Sleep(60 * time.Millisecond)
	require.NoError(t, failing.Stop(context.Background()))
	n := failures.Load()
	assert.GreaterOrEqual(t, n, int32(2))
	assert.LessOrEqual(t, n, int32(6), "errors back off exponentially instead of spinning")
}

func TestLoopStopHonoursDeadline(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	stuck := NewLoop("stuck", discard, time.Millisecond, func(context.Context) (bool, error) {
		<-release
		return false, nil
	})
	require.NoError(t, stuck.Start(context.Background()))
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := stuck.Stop(ctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	assert.NoError(t, NewLoop("never-started", discard, time.Second, nil).Stop(context.Background()))
}

func TestFaultTrigger(t *testing.T) {
	t.Parallel()

	var exited atomic.Int32
	f := NewFault(FaultConsumerAfterCommit, discard)
	f.exit = func(int) { exited.Add(1) }

	f.Trigger(FaultOutboxAfterPublish)
	assert.Equal(t, int32(0), exited.Load())
	f.Trigger(FaultConsumerAfterCommit)
	assert.Equal(t, int32(1), exited.Load())

	var nilFault *Fault
	nilFault.Trigger(FaultConsumerAfterCommit)
	NewFault("", discard).Trigger(FaultConsumerAfterCommit)
}
