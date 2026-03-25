package main

import (
	"strings"
	"testing"
	"time"
)

type blockingReadCloser struct {
	done chan struct{}
}

func (r *blockingReadCloser) Read(p []byte) (int, error) {
	<-r.done
	return 0, contextCanceledError{}
}

func (r *blockingReadCloser) Close() error {
	select {
	case <-r.done:
	default:
		close(r.done)
	}
	return nil
}

type contextCanceledError struct{}

func (contextCanceledError) Error() string { return "context canceled" }

func TestRequestTraceTracksChunkGapAndIdleTimeout(t *testing.T) {
	trace := &requestTrace{
		cfg:       requestTraceConfig{requests: true, packets: true, stallGap: 5 * time.Millisecond},
		reqID:     "req-gap",
		startedAt: time.Now(),
	}

	trace.noteResponseChunk(32)
	time.Sleep(8 * time.Millisecond)
	trace.noteResponseChunk(16)
	trace.noteIdleTimeout(25 * time.Millisecond)

	if trace.maxChunkGap < 5*time.Millisecond {
		t.Fatalf("max_chunk_gap=%v", trace.maxChunkGap)
	}
	if !trace.idleTimedOut {
		t.Fatal("expected idle timeout to be recorded")
	}
	if trace.idleTimeoutDuration != 25*time.Millisecond {
		t.Fatalf("idle_timeout_duration=%v", trace.idleTimeoutDuration)
	}
}

func TestIdleTimeoutReaderReturnsHelpfulIdleTimeout(t *testing.T) {
	rc := &blockingReadCloser{done: make(chan struct{})}
	cancelCalled := make(chan struct{}, 1)
	timeoutCalled := make(chan struct{}, 1)

	reader := newIdleTimeoutReader(rc, 15*time.Millisecond, func() {
		select {
		case cancelCalled <- struct{}{}:
		default:
		}
		_ = rc.Close()
	}, func() {
		select {
		case timeoutCalled <- struct{}{}:
		default:
		}
	})
	defer reader.Close()

	_, err := reader.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("expected idle timeout error")
	}
	if !strings.Contains(err.Error(), "idle for") {
		t.Fatalf("unexpected error: %v", err)
	}

	select {
	case <-timeoutCalled:
	default:
		t.Fatal("expected timeout callback to fire")
	}

	select {
	case <-cancelCalled:
	default:
		t.Fatal("expected cancel callback to fire")
	}
}
