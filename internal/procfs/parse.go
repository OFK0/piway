package procfs

import (
	"bytes"
	"errors"
	"fmt"
	"math"
)

// NextLine splits data at the first newline. The newline is dropped.
func NextLine(data []byte) (line, rest []byte) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return data[:i], data[i+1:]
	}
	return data, nil
}

// NextField skips leading spaces and returns the next space-separated field.
// field is empty when b holds no further fields.
func NextField(b []byte) (field, rest []byte) {
	i := 0
	for i < len(b) && b[i] == ' ' {
		i++
	}
	start := i
	for i < len(b) && b[i] != ' ' {
		i++
	}
	return b[start:i], b[i:]
}

var errEmpty = errors.New("empty number")

// ParseUint parses an unsigned decimal integer without allocating,
// unlike strconv.ParseUint(string(b), ...) which copies b into a string.
func ParseUint(b []byte) (uint64, error) {
	if len(b) == 0 {
		return 0, errEmpty
	}
	var v uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("unexpected byte %q in %q", c, b)
		}
		d := uint64(c - '0')
		if v > (math.MaxUint64-d)/10 {
			return 0, fmt.Errorf("number %q overflows uint64", b)
		}
		v = v*10 + d
	}
	return v, nil
}
