package stream

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Timeouts for one connection. A client that misses them is closed; the Hub
// independently evicts it if its queue fills up meanwhile.
const (
	writeTimeout = 5 * time.Second
	pingInterval = 15 * time.Second
	pingTimeout  = 5 * time.Second
)

// Server is the HTTP side of a Hub: it upgrades requests to WebSockets and
// streams the Hub's messages to them.
type Server struct {
	hub *Hub
	log *slog.Logger
	// conns tracks open connections. http.Server.Shutdown neither closes nor
	// waits for hijacked connections, so Wait does it instead.
	conns sync.WaitGroup
}

// NewServer returns an http.Handler for hub.
func NewServer(hub *Hub, log *slog.Logger) *Server {
	return &Server{hub: hub, log: log}
}

// Wait blocks until every connection has finished its close handshake.
// Call it after the Hub's Run has returned, which tells them to close.
func (s *Server) Wait() {
	s.conns.Wait()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Accept rejects cross-origin requests by default, which is what we want
	// for an unauthenticated endpoint. It writes the error response itself.
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	s.conns.Add(1)
	defer s.conns.Done()
	defer conn.CloseNow() // no-op after a successful Close

	c := s.hub.subscribe()
	defer s.hub.unsubscribe(c)

	// The stream is one-way. CloseRead discards anything the client sends,
	// answers pings and the close handshake, and cancels ctx when the client
	// goes away. Ping needs this reader to see the pong.
	ctx := conn.CloseRead(r.Context())

	err = s.pump(ctx, conn, c)
	switch {
	case err == nil:
		// c.send was closed by the Hub.
		code, reason := closeStatus(c.reason)
		_ = conn.Close(code, reason)
	case errors.Is(err, context.Canceled), websocket.CloseStatus(err) != -1:
		// The client left; nothing to log.
	default:
		s.log.Debug("stream client dropped", "remote", r.RemoteAddr, "err", err)
	}
}

// pump writes queued messages and keepalive pings until the Hub closes the
// queue (nil) or the connection fails (non-nil).
func (s *Server) pump(ctx context.Context, conn *websocket.Conn, c *client) error {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()

	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				return nil
			}
			if err := write(ctx, conn, msg); err != nil {
				return err
			}
		case <-ping.C:
			// Detects half-open connections (a laptop that went to sleep),
			// which TCP alone may not notice for many minutes.
			pctx, cancel := context.WithTimeout(ctx, pingTimeout)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func write(ctx context.Context, conn *websocket.Conn, msg []byte) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, msg)
}

func closeStatus(r closeReason) (websocket.StatusCode, string) {
	switch r {
	case reasonSlow:
		// 1013: the client may reconnect, ideally after a backoff.
		return websocket.StatusTryAgainLater, "too slow"
	case reasonShutdown:
		return websocket.StatusGoingAway, "server shutting down"
	default:
		return websocket.StatusNormalClosure, ""
	}
}
