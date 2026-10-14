// Package stream pushes every new snapshot to all connected WebSocket clients.
//
// The Hub is the only owner of the set of clients. Everything else talks to
// it through channels, so the client map needs no mutex:
//
//	agent ──Broadcast──▶ Hub.Run ──client.send──▶ writer pump ──▶ socket
//	handler ─subscribe/unsubscribe─▶ Hub.Run
//
// A client that cannot keep up is evicted instead of slowing everyone else
// down: its send queue is closed, and its writer pump closes the connection.
package stream

import "context"

// client is one subscriber as the Hub sees it. The WebSocket side wraps it.
type client struct {
	// send is the outbound queue. Only the Hub writes to and closes it; the
	// writer pump reads from it until it is closed.
	send chan []byte
	// reason says why the Hub closed send. It is written before the close,
	// and the close happens-before the receive that observes it, so the
	// writer pump may read it without a lock once send is closed.
	reason closeReason
}

type closeReason int

const (
	reasonLeft     closeReason = iota // the client unsubscribed itself
	reasonSlow                        // evicted: its queue was full
	reasonShutdown                    // the Hub stopped
)

// Hub fans out messages to subscribed clients.
type Hub struct {
	queueLen int

	register   chan *client
	unregister chan *client
	broadcast  chan []byte
	count      chan chan int
	// done is closed when Run returns.
	done chan struct{}

	// Owned by the Run goroutine. Nothing else may touch them.
	clients map[*client]struct{}
	last    []byte // most recent broadcast, replayed to new clients
}

// NewHub returns a Hub whose clients may fall at most queueLen messages
// behind before they are evicted. queueLen is raised to 1 if smaller.
func NewHub(queueLen int) *Hub {
	return &Hub{
		queueLen:   max(queueLen, 1),
		register:   make(chan *client),
		unregister: make(chan *client),
		broadcast:  make(chan []byte),
		count:      make(chan chan int),
		done:       make(chan struct{}),
		clients:    make(map[*client]struct{}),
	}
}

// Run serves the Hub until ctx is cancelled. On return every client's send
// queue has been closed.
func (h *Hub) Run(ctx context.Context) {
	// Deferred calls run last-in first-out: queues are closed before done,
	// so anyone woken by done already sees their queue closed.
	defer close(h.done)
	defer func() {
		for c := range h.clients {
			h.drop(c, reasonShutdown)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case c := <-h.register:
			h.clients[c] = struct{}{}
			if h.last != nil {
				// The queue is new and queueLen >= 1, so this cannot block.
				// A dashboard shows data at once instead of after one interval.
				c.send <- h.last
			}
		case c := <-h.unregister:
			h.drop(c, reasonLeft)
		case reply := <-h.count:
			reply <- len(h.clients)
		case msg := <-h.broadcast:
			h.last = msg
			for c := range h.clients {
				select {
				case c.send <- msg:
				default:
					// Queue full: the client is queueLen messages behind.
					// Waiting would stall every other client, so evict it.
					h.drop(c, reasonSlow)
				}
			}
		}
	}
}

// drop removes c and closes its queue. Membership in clients is what makes
// the close happen exactly once, whether c leaves or is evicted first.
func (h *Hub) drop(c *client, why closeReason) {
	if _, ok := h.clients[c]; !ok {
		return
	}
	delete(h.clients, c)
	c.reason = why
	close(c.send)
}

// Broadcast queues msg for every current client. It never blocks for long:
// the agent calls it on its collection loop. msg must not be modified after
// the call; all clients share the same slice.
func (h *Hub) Broadcast(msg []byte) {
	// Run never blocks inside a broadcast round (sends to client queues are
	// non-blocking), so this waits at most one fan-out loop.
	select {
	case h.broadcast <- msg:
	case <-h.done:
	}
}

// subscribe registers a new client. The most recent broadcast, if any, is
// already queued for it. After Run has returned it returns a
// client whose send queue is already closed, so callers never hang.
func (h *Hub) subscribe() *client {
	c := &client{send: make(chan []byte, h.queueLen)}
	select {
	case h.register <- c:
	case <-h.done:
		c.reason = reasonShutdown
		close(c.send)
	}
	return c
}

// clientCount returns the number of subscribed clients, or 0 after Run has
// returned. Like every other read of clients, it is answered by Run.
func (h *Hub) clientCount() int {
	reply := make(chan int, 1)
	select {
	case h.count <- reply:
		return <-reply
	case <-h.done:
		return 0
	}
}

// unsubscribe removes c. It is safe to call more than once, after the Hub
// has evicted c, and after Run has returned.
func (h *Hub) unsubscribe(c *client) {
	select {
	case h.unregister <- c:
	case <-h.done:
	}
}
