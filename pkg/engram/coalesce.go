package engram

import (
	"sync"
	"time"
)

type coalesceBuffer struct {
	mu          sync.Mutex
	chunks      []turnChunk
	firstAdded  time.Time
	window      time.Duration
	maxDuration time.Duration
	timer       *time.Timer
	maxTimer    *time.Timer
	emitFn      func(turnChunk)
	stopped     bool
}

func newCoalesceBuffer(window, maxDuration time.Duration, emitFn func(turnChunk)) *coalesceBuffer {
	return &coalesceBuffer{
		window:      window,
		maxDuration: maxDuration,
		emitFn:      emitFn,
	}
}

func (cb *coalesceBuffer) add(chunk turnChunk) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.stopped {
		return
	}

	// Passthrough when coalescing is disabled
	if cb.window <= 0 {
		cb.emitFn(chunk)
		return
	}

	if len(cb.chunks) == 0 {
		cb.firstAdded = time.Now()
		// Start a max-duration timer that will force-flush even if new
		// chunks keep arriving within the coalesce window.
		if cb.maxDuration > 0 {
			cb.maxTimer = time.AfterFunc(cb.maxDuration, func() {
				cb.mu.Lock()
				defer cb.mu.Unlock()
				if !cb.stopped && len(cb.chunks) > 0 {
					if cb.timer != nil {
						cb.timer.Stop()
					}
					cb.emitMergedLocked()
				}
			})
		}
	}
	cb.chunks = append(cb.chunks, chunk)

	// Force flush if max duration already exceeded (e.g. slow add calls)
	if cb.maxDuration > 0 && time.Since(cb.firstAdded) >= cb.maxDuration {
		if cb.maxTimer != nil {
			cb.maxTimer.Stop()
		}
		cb.emitMergedLocked()
		return
	}

	// Reset coalesce timer
	if cb.timer != nil {
		cb.timer.Stop()
	}
	cb.timer = time.AfterFunc(cb.window, func() {
		cb.mu.Lock()
		defer cb.mu.Unlock()
		if !cb.stopped && len(cb.chunks) > 0 {
			cb.emitMergedLocked()
		}
	})
}

func (cb *coalesceBuffer) flush() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.timer != nil {
		cb.timer.Stop()
	}
	if cb.maxTimer != nil {
		cb.maxTimer.Stop()
	}
	if len(cb.chunks) > 0 {
		cb.emitMergedLocked()
	}
}

func (cb *coalesceBuffer) stop() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.stopped = true
	if cb.timer != nil {
		cb.timer.Stop()
	}
	if cb.maxTimer != nil {
		cb.maxTimer.Stop()
	}
}

func (cb *coalesceBuffer) emitMergedLocked() {
	if len(cb.chunks) == 0 {
		return
	}
	merged := cb.chunks[0]
	for _, c := range cb.chunks[1:] {
		merged.PCM = append(merged.PCM, c.PCM...)
	}
	cb.chunks = nil
	cb.emitFn(merged)
}
