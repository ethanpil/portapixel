package httpd

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// The events of /api/player/events (ARCHITECTURE 7a).
const (
	// EventPlaylist tells the player to ask for the manifest again and start at
	// item 0.
	EventPlaylist = "playlist"
	// EventGrace asks the player for a good moment to restart the browser. The
	// player answers with POST /api/player/ready.
	EventGrace = "grace"
	// EventReload tells the player to load the page again now.
	EventReload = "reload"
)

// keepAlive is the time between two comment lines on an idle stream. A proxy or a
// phone network closes a connection that says nothing, and the player would then
// miss a playlist change until its own reconnect.
const keepAlive = 20 * time.Second

// hubQueue is how many events one subscriber may fall behind. The player is on
// the same machine and reads at once; a queue of four covers a moment of load,
// and after that the oldest event goes, because the newest event is the one that
// matters.
const hubQueue = 4

// Hub is the server-sent-event stream of the player. EventSource reconnects by
// itself, so there is no retry logic on either end: that was the whole reason to
// use it in place of a long poll (plan section 8).
type Hub struct {
	mu     sync.Mutex
	subs   map[int]chan sseEvent
	nextID int
	closed chan struct{}
	// replay makes the hub send its last event to a new subscriber. last holds it.
	replay bool
	last   sseEvent
}

type sseEvent struct {
	name string
	data []byte
}

// NewHub makes an empty hub.
func NewHub() *Hub {
	return &Hub{subs: make(map[int]chan sseEvent), closed: make(chan struct{})}
}

// NewReplayHub makes a hub that sends its last event to a new subscriber.
//
// The install onto a disk needs it. The admin UI sends POST /api/install-to-disk
// and opens the event stream after the answer, so the first events are already
// gone. Without the replay a fast step, and worse a "done" event, would never reach
// the page, and the progress bar would stand still for ever.
func NewReplayHub() *Hub {
	h := NewHub()
	h.replay = true
	return h
}

// Close ends every open stream. The daemon calls it before it drains the HTTP
// server.
//
// http.Server.Shutdown waits for each request to end and does not cancel a
// request context. The stream of the player never ends by itself, so without this
// every stop of the daemon — a service restart, an update, a reboot — cost the
// full shutdown grace.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	select {
	case <-h.closed:
		return // Close twice must be safe: a stop can come from two paths.
	default:
	}
	close(h.closed)
}

// Reset forgets the event that a replay hub holds. A caller that starts a new run
// of work calls it BEFORE the work starts.
//
// Without it the "done" event of the install before was the first thing that a new
// subscriber received. The admin UI sends the POST, then opens the stream, and its
// done handler closes the stream and prints "The disk is ready. Power the machine
// off and take the stick out" — while the second install was writing the partition
// table. A person who acts on that message pulls the stick in the middle of a real
// write.
func (h *Hub) Reset() {
	h.mu.Lock()
	h.last = sseEvent{}
	h.mu.Unlock()
}

// Send gives one event to every player that listens. data may be nil.
func (h *Hub) Send(name string, data any) {
	payload := []byte("{}")
	if data != nil {
		if encoded, err := json.Marshal(data); err == nil {
			payload = encoded
		}
	}
	event := sseEvent{name: name, data: payload}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.replay {
		h.last = event
	}
	for _, ch := range h.subs {
		select {
		case ch <- event:
		default:
			// The subscriber is behind. Drop its oldest event and put the new one
			// in its place.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- event:
			default:
			}
		}
	}
}

// Listeners gives the number of open streams. The status page shows it, and a
// test uses it to wait for the player.
func (h *Hub) Listeners() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// serve is the handler of GET /api/player/events.
func (h *Hub) serve(w http.ResponseWriter, r *http.Request) {
	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		writeError(w, http.StatusInternalServerError, "this server cannot stream events")
		return
	}

	ch, cancel := h.subscribe()
	defer cancel()

	h.mu.Lock()
	closed := h.closed
	last := h.last
	h.mu.Unlock()
	select {
	case <-closed:
		// The daemon is stopping. A stream that opens now would hold the
		// shutdown for the whole grace time.
		writeError(w, http.StatusServiceUnavailable, "the daemon is stopping")
		return
	default:
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	// A first comment makes the browser call the stream open, so the player knows
	// that it is connected.
	w.Write([]byte(": connected\n\n"))
	if last.name != "" {
		w.Write([]byte("event: " + last.name + "\ndata: " + string(last.data) + "\n\n"))
	}
	flusher.Flush()

	tick := time.NewTicker(keepAlive)
	defer tick.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-closed:
			return
		case <-tick.C:
			if _, err := w.Write([]byte(": keep alive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case event := <-ch:
			if _, err := w.Write([]byte("event: " + event.name + "\ndata: " + string(event.data) + "\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// subscribe adds a listener and gives the function that removes it.
func (h *Hub) subscribe() (<-chan sseEvent, func()) {
	ch := make(chan sseEvent, hubQueue)

	h.mu.Lock()
	id := h.nextID
	h.nextID++
	h.subs[id] = ch
	h.mu.Unlock()

	return ch, func() {
		h.mu.Lock()
		delete(h.subs, id)
		h.mu.Unlock()
	}
}
