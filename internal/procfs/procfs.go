// Package procfs reads procfs and sysfs files repeatedly without reopening them.
package procfs

import (
	"errors"
	"io"
	"os"
)

// File is a procfs or sysfs file kept open between reads.
//
// Both filesystems regenerate a file's content whenever it is read from offset 0, so
// re-reading the same descriptor returns fresh data without an open/close per
// sample. A File is not safe for concurrent use.
type File struct {
	f   *os.File
	buf []byte
}

// Open opens path for repeated reads.
func Open(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &File{f: f, buf: make([]byte, 0, 4096)}, nil
}

// Read returns the current content of the file. The returned slice is only
// valid until the next call to Read; the buffer grows only when needed.
func (p *File) Read() ([]byte, error) {
	buf := p.buf[:0]
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, err := p.f.ReadAt(buf[len(buf):cap(buf)], int64(len(buf)))
		buf = buf[:len(buf)+n]
		if errors.Is(err, io.EOF) {
			p.buf = buf
			return buf, nil
		}
		if err != nil {
			p.buf = buf
			return nil, err
		}
	}
}

// Close releases the underlying file.
func (p *File) Close() error {
	return p.f.Close()
}
