package server

import "sync"

// maxHistoryFrames caps how many SSE frames a Broadcaster replays to a
// newly-subscribing client. A typical plan/apply run produces at most a
// few hundred lines of output; this is generous headroom without letting
// a pathological amount of output grow memory unboundedly for a
// long-running server process.
const maxHistoryFrames = 4000

// GapFrame formats a notice frame telling a client that dropped frames
// never reached it — either trimmed from history before it could ever be
// replayed, or dropped live because its subscriber channel was full (a
// slow or stalled browser tab). Broadcaster stays agnostic of SSE
// framing itself (see the package doc below), so the caller supplies
// this rather than Broadcaster hardcoding an event name/payload shape.
// A nil GapFrame disables gap notices entirely — frames are still
// dropped the same way, there's just nothing to tell a client about it.
type GapFrame func(dropped int) []byte

// Broadcaster fans pre-formatted SSE frames out to any number of
// subscribers, and replays everything published so far to a client that
// subscribes mid-run (or reconnects) — e.g. someone refreshing the job
// page partway through a plan still sees the output from before they
// loaded the page, not just what happens next.
//
// It knows nothing about SSE framing itself (event names, JSON payloads)
// — callers pass it fully-formatted bytes. That keeps it reusable and
// trivially testable without an HTTP server in the loop.
type Broadcaster struct {
	gapFrame GapFrame

	mu             sync.Mutex
	history        [][]byte
	historyDropped int // frames trimmed from history before any client could ever replay them
	subs           map[chan []byte]struct{}
	subDropped     map[chan []byte]int // per-subscriber frames dropped live, not yet flagged
	closed         bool
}

func NewBroadcaster(gapFrame GapFrame) *Broadcaster {
	return &Broadcaster{
		gapFrame:   gapFrame,
		subs:       map[chan []byte]struct{}{},
		subDropped: map[chan []byte]int{},
	}
}

// Publish sends frame to every current subscriber and appends it to the
// replay history. A no-op once Close has been called. A slow subscriber
// (channel full) has this frame dropped for it rather than blocking the
// publisher — a laggy browser tab must never be able to stall a real
// terraform apply — but the drop is counted and, once room opens back up
// (or the next Subscribe/replay happens for history drops), reported via
// gapFrame rather than silently leaving a gap no one can see.
func (b *Broadcaster) Publish(frame []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.history = append(b.history, frame)
	if len(b.history) > maxHistoryFrames {
		b.history = b.history[len(b.history)-maxHistoryFrames:]
		b.historyDropped++
	}
	for ch := range b.subs {
		// Try to flush any pending gap notice first, on every Publish —
		// not just ones whose own frame happens to fit — so a subscriber
		// that stops receiving new content (the run's very last frames
		// were what got dropped) still gets a chance to see the notice on
		// the next call rather than only when a frame lands cleanly.
		b.flushGap(ch)
		select {
		case ch <- frame:
		default:
			b.subDropped[ch]++
		}
	}
}

// flushGap opportunistically delivers a gap notice for ch, resetting its
// drop count only on success — if the channel is still full, the count
// is left in place for the next Publish to try again rather than being
// lost.
func (b *Broadcaster) flushGap(ch chan []byte) {
	if b.gapFrame == nil {
		return
	}
	dropped := b.subDropped[ch]
	if dropped == 0 {
		return
	}
	select {
	case ch <- b.gapFrame(dropped):
		delete(b.subDropped, ch)
	default:
	}
}

// Subscribe registers a new live listener and atomically returns
// everything published so far, so a caller can replay history then
// switch to reading from live without a gap or a duplicate — the lock
// is held across snapshotting history and registering the channel,
// so nothing published concurrently can be missed or double-delivered.
// If the broadcaster is already closed, live is returned already closed
// and replay is still the full history. If frames were ever trimmed from
// history before this subscriber could see them, a gap notice (via
// gapFrame) is prepended to replay so the client knows its replay isn't
// complete, rather than it looking like the run simply started later
// than it did.
func (b *Broadcaster) Subscribe() (replay [][]byte, live chan []byte) {
	ch := make(chan []byte, 256)
	b.mu.Lock()
	defer b.mu.Unlock()
	replay = append([][]byte(nil), b.history...)
	if b.historyDropped > 0 && b.gapFrame != nil {
		replay = append([][]byte{b.gapFrame(b.historyDropped)}, replay...)
	}
	if b.closed {
		close(ch)
		return replay, ch
	}
	b.subs[ch] = struct{}{}
	return replay, ch
}

// Unsubscribe removes a listener registered via Subscribe. Safe to call
// more than once or after Close.
func (b *Broadcaster) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		delete(b.subDropped, ch)
		close(ch)
	}
}

// Close marks the broadcaster done: every current subscriber's channel
// is closed (signaling "no more data"), and any later Subscribe gets an
// already-closed channel plus the final history. Safe to call more than
// once.
func (b *Broadcaster) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for ch := range b.subs {
		close(ch)
	}
	b.subs = map[chan []byte]struct{}{}
	b.subDropped = map[chan []byte]int{}
}
