package cmd

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestParentBoundContextCancelsWhenSSHParentDisappears(t *testing.T) {
	var parent atomic.Int64
	parent.Store(42)
	ctx, cancel := parentBoundContext(context.Background(), func() int { return int(parent.Load()) }, time.Millisecond)
	defer cancel()
	parent.Store(1)
	select {
	case <-ctx.Done():
	case <-time.After(250 * time.Millisecond):
		t.Fatal("context remained active after parent became init")
	}
}

func TestParentBoundContextHonorsCallerCancellation(t *testing.T) {
	base, stop := context.WithCancel(context.Background())
	ctx, cancel := parentBoundContext(base, func() int { return 42 }, time.Millisecond)
	defer cancel()
	stop()
	select {
	case <-ctx.Done():
	case <-time.After(250 * time.Millisecond):
		t.Fatal("context ignored caller cancellation")
	}
}
