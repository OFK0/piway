package deploy

import (
	"os"
	"sync"
)

// buildLog is the output of one running build: appended to <id>.log and
// fanned out to live subscribers.
type buildLog struct {
	mu     sync.Mutex
	f      *os.File
	subs   map[chan []byte]struct{}
	closed bool
	lost   error // the first error writing the file: the log is incomplete
}

func openLog(path string) (*buildLog, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &buildLog{f: f, subs: map[chan []byte]struct{}{}}, nil
}

// Write appends p and sends a copy to every subscriber. A subscriber that
// has fallen 256 writes behind is dropped (its channel closed) rather than
// slowing the build down; it can reread the file.
//
// A file that cannot be written (the disk is full) does not fail the write:
// the process writing here, a docker build, would stop reading its output
// and block. Subscribers still get everything; the first error is kept in
// lost.
func (l *buildLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.f.Write(p); err != nil && l.lost == nil {
		l.lost = err
	}
	chunk := append([]byte(nil), p...)
	for ch := range l.subs {
		select {
		case ch <- chunk:
		default:
			delete(l.subs, ch)
			close(ch)
		}
	}
	return len(p), nil
}

// incomplete is the first error that kept output out of the file, if any.
func (l *buildLog) incomplete() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lost
}

// subscribe returns everything written so far and a channel of what comes
// next; both under the lock, so nothing is lost or repeated between them.
// The channel is closed when the build ends.
func (l *buildLog) subscribe(path string) ([]byte, chan []byte, func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	past, _ := os.ReadFile(path)
	ch := make(chan []byte, 256)
	if l.closed {
		close(ch)
		return past, ch, func() {}
	}
	l.subs[ch] = struct{}{}
	return past, ch, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if _, ok := l.subs[ch]; ok {
			delete(l.subs, ch)
			close(ch)
		}
	}
}

// close ends the log: subscribers' channels close, the file is closed.
func (l *buildLog) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	for ch := range l.subs {
		close(ch)
	}
	l.subs = nil
	return l.f.Close()
}
