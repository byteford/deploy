package server

import (
	"testing"
	"time"
)

func TestBroadcaster_PublishThenSubscribe_ReplaysHistory(t *testing.T) {
	b := NewBroadcaster(nil)
	b.Publish([]byte("one"))
	b.Publish([]byte("two"))

	replay, live := b.Subscribe()
	defer b.Unsubscribe(live)

	if len(replay) != 2 || string(replay[0]) != "one" || string(replay[1]) != "two" {
		t.Fatalf("replay = %v, want [one two]", stringsOf(replay))
	}
}

func TestBroadcaster_LiveDeliveryAfterSubscribe(t *testing.T) {
	b := NewBroadcaster(nil)
	_, live := b.Subscribe()
	defer b.Unsubscribe(live)

	b.Publish([]byte("hello"))

	select {
	case frame := <-live:
		if string(frame) != "hello" {
			t.Errorf("got %q, want hello", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live frame")
	}
}

func TestBroadcaster_MultipleSubscribersAllGetLiveFrames(t *testing.T) {
	b := NewBroadcaster(nil)
	_, live1 := b.Subscribe()
	_, live2 := b.Subscribe()
	defer b.Unsubscribe(live1)
	defer b.Unsubscribe(live2)

	b.Publish([]byte("x"))

	for i, ch := range []chan []byte{live1, live2} {
		select {
		case frame := <-ch:
			if string(frame) != "x" {
				t.Errorf("subscriber %d got %q, want x", i, frame)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out", i)
		}
	}
}

func TestBroadcaster_CloseClosesLiveChannelsAndStopsAcceptingPublishes(t *testing.T) {
	b := NewBroadcaster(nil)
	_, live := b.Subscribe()

	b.Close()

	select {
	case _, ok := <-live:
		if ok {
			t.Error("expected the live channel to be closed after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel close")
	}

	b.Publish([]byte("after close")) // must be a silent no-op, not a panic
}

func TestBroadcaster_SubscribeAfterCloseGetsHistoryAndClosedChannel(t *testing.T) {
	b := NewBroadcaster(nil)
	b.Publish([]byte("before close"))
	b.Close()

	replay, live := b.Subscribe()
	if len(replay) != 1 || string(replay[0]) != "before close" {
		t.Fatalf("replay = %v, want [before close]", stringsOf(replay))
	}
	if _, ok := <-live; ok {
		t.Error("expected an already-closed channel for a post-Close subscriber")
	}
}

func TestBroadcaster_UnsubscribeIsIdempotentAndSafeAfterClose(t *testing.T) {
	b := NewBroadcaster(nil)
	_, live := b.Subscribe()
	b.Unsubscribe(live)
	b.Unsubscribe(live) // must not panic (double-close guarded)

	_, live2 := b.Subscribe()
	b.Close()
	b.Unsubscribe(live2) // must not panic after Close either
}

func TestBroadcaster_SlowSubscriberFrameIsDroppedNotBlocking(t *testing.T) {
	b := NewBroadcaster(nil)
	_, live := b.Subscribe()
	defer b.Unsubscribe(live)

	// Fill the subscriber's buffer without ever reading it, then publish
	// one more — Publish must return promptly (not deadlock) even though
	// the subscriber's channel is full and nobody is draining it.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			b.Publish([]byte("spam"))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Publish blocked on a slow/unread subscriber instead of dropping frames")
	}
}

func TestBroadcaster_HistoryCapped(t *testing.T) {
	b := NewBroadcaster(nil)
	for i := 0; i < maxHistoryFrames+50; i++ {
		b.Publish([]byte("x"))
	}
	replay, live := b.Subscribe()
	defer b.Unsubscribe(live)
	if len(replay) != maxHistoryFrames {
		t.Errorf("history length = %d, want capped at %d", len(replay), maxHistoryFrames)
	}
}

// countingGapFrame returns a GapFrame that records every value it was
// called with (in order) into calls, and formats a frame that just
// stringifies the dropped count so tests can assert on it without
// depending on any particular wire format.
func countingGapFrame(calls *[]int) GapFrame {
	return func(dropped int) []byte {
		*calls = append(*calls, dropped)
		return []byte("gap:" + string(rune('0'+dropped)))
	}
}

func TestBroadcaster_HistoryOverflowPrependsGapNoticeToReplay(t *testing.T) {
	var calls []int
	b := NewBroadcaster(countingGapFrame(&calls))
	for i := 0; i < maxHistoryFrames+7; i++ {
		b.Publish([]byte("x"))
	}

	replay, live := b.Subscribe()
	defer b.Unsubscribe(live)

	if len(replay) != maxHistoryFrames+1 {
		t.Fatalf("replay length = %d, want %d (history + 1 gap notice)", len(replay), maxHistoryFrames+1)
	}
	if string(replay[0]) != "gap:7" {
		t.Errorf("first replay frame = %q, want a gap notice reporting 7 dropped", replay[0])
	}
	for _, f := range replay[1:] {
		if string(f) != "x" {
			t.Errorf("expected only history frames after the gap notice, got %q", f)
		}
	}
}

func TestBroadcaster_NoHistoryOverflowMeansNoGapNotice(t *testing.T) {
	var calls []int
	b := NewBroadcaster(countingGapFrame(&calls))
	b.Publish([]byte("one"))
	b.Publish([]byte("two"))

	replay, live := b.Subscribe()
	defer b.Unsubscribe(live)

	if len(replay) != 2 {
		t.Fatalf("replay = %v, want exactly the 2 published frames, no gap notice", stringsOf(replay))
	}
	if len(calls) != 0 {
		t.Errorf("gapFrame should not have been called, got calls=%v", calls)
	}
}

func TestBroadcaster_NilGapFrameSkipsHistoryNoticeSilently(t *testing.T) {
	// Same overflow as above, but with no GapFrame configured — dropping
	// still happens, there's just nothing to report it with. Must not
	// panic and must not fabricate a notice frame.
	b := NewBroadcaster(nil)
	for i := 0; i < maxHistoryFrames+7; i++ {
		b.Publish([]byte("x"))
	}
	replay, live := b.Subscribe()
	defer b.Unsubscribe(live)
	if len(replay) != maxHistoryFrames {
		t.Errorf("replay length = %d, want %d (no gap notice without a GapFrame)", len(replay), maxHistoryFrames)
	}
}

func TestBroadcaster_SlowSubscriberEventuallySeesGapNotice(t *testing.T) {
	var calls []int
	b := NewBroadcaster(countingGapFrame(&calls))
	_, live := b.Subscribe()
	defer b.Unsubscribe(live)

	// Fill the subscriber's 256-slot buffer without draining it, forcing
	// drops.
	for i := 0; i < 300; i++ {
		b.Publish([]byte("spam"))
	}

	// Drain one frame to open a slot, then publish once more — mirroring
	// how a real job always emits at least one more event (a status
	// line, then job_done) after any burst of output. That next Publish
	// is what actually gets a chance to flush the pending gap notice into
	// the newly-freed slot.
	<-live
	b.Publish([]byte("more"))

	var sawGap bool
	deadline := time.After(2 * time.Second)
	for !sawGap {
		select {
		case frame := <-live:
			if len(frame) >= 4 && string(frame[:4]) == "gap:" {
				sawGap = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for a gap notice after a slow subscriber dropped frames")
		}
	}
}

func stringsOf(frames [][]byte) []string {
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i] = string(f)
	}
	return out
}
