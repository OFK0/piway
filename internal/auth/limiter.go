package auth

import (
	"sync"
	"time"
)

// Wrong passwords from one client address: the first freeFails cost
// nothing extra, then each one locks the client out twice as long as the
// one before, from lockStart up to lockMax. A success, or forgetAfter
// without a failure, starts over.
const (
	freeFails   = 5
	lockStart   = 30 * time.Second
	lockMax     = time.Hour
	forgetAfter = 24 * time.Hour
	maxClients  = 10000 // bounds memory under a spray of addresses
)

type limiter struct {
	mu      sync.Mutex
	clients map[string]*failures
}

type failures struct {
	n     int
	last  time.Time
	until time.Time
}

func newLimiter() *limiter { return &limiter{clients: map[string]*failures{}} }

// wait is how long client must wait before trying again.
func (l *limiter) wait(client string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if f, ok := l.clients[client]; ok && now.Before(f.until) {
		return f.until.Sub(now)
	}
	return 0
}

// fail records a wrong password and returns the lockout it starts, if any.
func (l *limiter) fail(client string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.clients[client]
	if !ok || now.Sub(f.last) > forgetAfter {
		if len(l.clients) >= maxClients {
			l.forgetLocked(now)
		}
		f = &failures{}
		l.clients[client] = f
	}
	f.n++
	f.last = now
	if f.n < freeFails {
		return 0
	}
	lock := lockStart << min(f.n-freeFails, 20)
	lock = min(lock, lockMax)
	f.until = now.Add(lock)
	return lock
}

func (l *limiter) reset(client string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.clients, client)
}

// forgetLocked drops clients that are not locked out, oldest first if need be.
func (l *limiter) forgetLocked(now time.Time) {
	for k, f := range l.clients {
		if !now.Before(f.until) {
			delete(l.clients, k)
		}
	}
	for k := range l.clients { // still full: everyone is locked out
		if len(l.clients) < maxClients {
			break
		}
		delete(l.clients, k)
	}
}
