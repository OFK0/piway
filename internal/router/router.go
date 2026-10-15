// Package router owns each app's stable address and proxies it to whichever
// container currently serves the app.
//
// A deploy starts the new container on a port Docker picks, checks its
// health there, and only then points the app's route at it: one atomic
// pointer swap. Requests already in flight finish on the old container;
// new ones go to the new one. That is what makes a deploy zero-downtime on
// a single host (blue/green), and Caddy (#24) can keep proxying
// <name>.<domain> to the same stable port throughout.
//
// The trade-off: apps are reachable only while piway runs.
package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Router serves one listener per app on 127.0.0.1.
type Router struct {
	log      *slog.Logger
	mu       sync.Mutex
	routes   map[string]*route
	onChange func()
}

type route struct {
	port   int
	target atomic.Pointer[url.URL]
	srv    *http.Server
	done   chan struct{}
}

func New(log *slog.Logger) *Router {
	return &Router{log: log, routes: map[string]*route{}}
}

// OnChange registers f to run after an app gains or loses its route, not
// when a route merely switches containers. Call it before the first Set.
func (r *Router) OnChange(f func()) { r.onChange = f }

func (r *Router) changed() {
	if r.onChange != nil {
		r.onChange()
	}
}

// Ports returns each routed app's stable port.
func (r *Router) Ports() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int, len(r.routes))
	for app, rt := range r.routes {
		out[app] = rt.port
	}
	return out
}

// Set points app's stable port at target (http://127.0.0.1:<port>),
// listening on 127.0.0.1:port first if the app has no route yet.
func (r *Router) Set(app string, port int, target string) error {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return fmt.Errorf("router: bad target %q", target)
	}
	r.mu.Lock()
	if rt, ok := r.routes[app]; ok {
		defer r.mu.Unlock()
		if rt.port != port {
			return fmt.Errorf("router: %s already listens on %d", app, rt.port)
		}
		rt.target.Store(u)
		return nil
	}
	if err := r.listen(app, port, u); err != nil {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()
	r.changed()
	return nil
}

// listen starts app's route; r.mu is held.
func (r *Router) listen(app string, port int, u *url.URL) error {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("router: %s: %w", app, err)
	}
	rt := &route{port: port, done: make(chan struct{})}
	rt.target.Store(u)
	rt.srv = &http.Server{
		Handler:           r.proxy(app, rt),
		ReadHeaderTimeout: 10 * time.Second,
	}
	r.routes[app] = rt
	go func() {
		defer close(rt.done)
		if err := rt.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			r.log.Error("app route stopped", "app", app, "err", err)
		}
	}()
	return nil
}

func (r *Router) proxy(app string, rt *route) http.Handler {
	return &httputil.ReverseProxy{
		// Read the target per request, so a Set takes effect for the very
		// next request without dropping the ones in flight.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(rt.target.Load())
			pr.SetXForwarded()
			pr.Out.Host = pr.In.Host
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			r.log.Warn("app unreachable", "app", app, "err", err)
			http.Error(w, app+" is not responding", http.StatusBadGateway)
		},
	}
}

// Target returns where app's route points, or "" if it has none.
func (r *Router) Target(app string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rt, ok := r.routes[app]; ok {
		return rt.target.Load().String()
	}
	return ""
}

// Remove stops app's route.
func (r *Router) Remove(app string) {
	r.mu.Lock()
	rt, ok := r.routes[app]
	delete(r.routes, app)
	r.mu.Unlock()
	if ok {
		rt.srv.Close()
		<-rt.done
		r.changed()
	}
}

// Close stops every route, letting in-flight requests finish for up to
// five seconds.
func (r *Router) Close() error {
	r.mu.Lock()
	routes := r.routes
	r.routes = map[string]*route{}
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var errs []error
	for _, rt := range routes {
		if err := rt.srv.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
		<-rt.done
	}
	return errors.Join(errs...)
}
