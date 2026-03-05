package engram

import (
	"sync"
	"testing"
	"time"
)

func TestCoalesceBuffer_MergesRapidChunks(t *testing.T) {
	var emitted []turnChunk
	var mu sync.Mutex
	cb := newCoalesceBuffer(200*time.Millisecond, 30*time.Second, func(merged turnChunk) {
		mu.Lock()
		emitted = append(emitted, merged)
		mu.Unlock()
	})
	defer cb.stop()

	cb.add(turnChunk{PCM: []byte{1, 2}, SampleRate: 16000, Channels: 1})
	time.Sleep(50 * time.Millisecond)
	cb.add(turnChunk{PCM: []byte{3, 4}, SampleRate: 16000, Channels: 1})

	// Wait for coalesce window to expire
	time.Sleep(300 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(emitted) != 1 {
		t.Fatalf("expected 1 merged chunk, got %d", len(emitted))
	}
	if len(emitted[0].PCM) != 4 {
		t.Fatalf("expected 4 bytes merged PCM, got %d", len(emitted[0].PCM))
	}
}

func TestCoalesceBuffer_EmitsSeparateAfterGap(t *testing.T) {
	var emitted []turnChunk
	var mu sync.Mutex
	cb := newCoalesceBuffer(100*time.Millisecond, 30*time.Second, func(merged turnChunk) {
		mu.Lock()
		emitted = append(emitted, merged)
		mu.Unlock()
	})
	defer cb.stop()

	cb.add(turnChunk{PCM: []byte{1, 2}, SampleRate: 16000, Channels: 1})
	time.Sleep(200 * time.Millisecond) // gap > window
	cb.add(turnChunk{PCM: []byte{3, 4}, SampleRate: 16000, Channels: 1})
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(emitted) != 2 {
		t.Fatalf("expected 2 separate chunks, got %d", len(emitted))
	}
}

func TestCoalesceBuffer_MaxDurationForceFlush(t *testing.T) {
	var emitted []turnChunk
	var mu sync.Mutex
	cb := newCoalesceBuffer(500*time.Millisecond, 150*time.Millisecond, func(merged turnChunk) {
		mu.Lock()
		emitted = append(emitted, merged)
		mu.Unlock()
	})
	defer cb.stop()

	cb.add(turnChunk{PCM: []byte{1, 2}, SampleRate: 16000, Channels: 1})
	time.Sleep(100 * time.Millisecond)
	cb.add(turnChunk{PCM: []byte{3, 4}, SampleRate: 16000, Channels: 1})
	time.Sleep(100 * time.Millisecond)
	// Max duration (150ms) exceeded by now; should force flush despite window (500ms) not expired

	time.Sleep(100 * time.Millisecond) // let flush propagate

	mu.Lock()
	defer mu.Unlock()
	if len(emitted) < 1 {
		t.Fatalf("expected at least 1 forced flush, got %d", len(emitted))
	}
}

func TestCoalesceBuffer_FlushDrainsRemaining(t *testing.T) {
	var emitted []turnChunk
	var mu sync.Mutex
	cb := newCoalesceBuffer(5*time.Second, 30*time.Second, func(merged turnChunk) {
		mu.Lock()
		emitted = append(emitted, merged)
		mu.Unlock()
	})

	cb.add(turnChunk{PCM: []byte{1, 2, 3, 4}, SampleRate: 16000, Channels: 1})
	cb.flush() // force drain without waiting

	mu.Lock()
	defer mu.Unlock()
	if len(emitted) != 1 {
		t.Fatalf("expected 1 flushed chunk, got %d", len(emitted))
	}
}

func TestCoalesceBuffer_Disabled(t *testing.T) {
	// When window is 0, chunks should pass through without buffering
	var emitted []turnChunk
	var mu sync.Mutex
	cb := newCoalesceBuffer(0, 0, func(merged turnChunk) {
		mu.Lock()
		emitted = append(emitted, merged)
		mu.Unlock()
	})
	defer cb.stop()

	cb.add(turnChunk{PCM: []byte{1, 2}, SampleRate: 16000, Channels: 1})
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(emitted) != 1 {
		t.Fatalf("expected immediate emit when window=0, got %d", len(emitted))
	}
}
