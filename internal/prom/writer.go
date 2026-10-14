// Package prom serves piway's metrics in the Prometheus text exposition
// format, version 0.0.4:
// https://prometheus.io/docs/instrumenting/exposition_formats/
//
// The format is small enough to write by hand, which keeps the Prometheus
// client library and its dependencies out of the binary.
package prom

import (
	"bufio"
	"io"
	"math"
	"strconv"
	"strings"
)

// Metric types used here.
const (
	Counter = "counter"
	Gauge   = "gauge"
)

// Writer collects samples and renders them grouped by metric family, each
// family with one HELP and one TYPE line, in the order families were first
// added. A Writer is not safe for concurrent use.
type Writer struct {
	families []*family
	byName   map[string]*family
}

type family struct {
	name, typ, help string
	samples         []sample
}

type sample struct {
	labels []string // name, value, name, value, ...
	value  float64
}

// Add records one sample. labels alternate names and values. The first Add
// for a name fixes its type and help text.
func (w *Writer) Add(name, typ, help string, value float64, labels ...string) {
	if len(labels)%2 != 0 {
		panic("prom: labels must be name/value pairs")
	}
	f := w.byName[name]
	if f == nil {
		if w.byName == nil {
			w.byName = make(map[string]*family)
		}
		f = &family{name: name, typ: typ, help: help}
		w.byName[name] = f
		w.families = append(w.families, f)
	}
	f.samples = append(f.samples, sample{labels: labels, value: value})
}

// WriteTo renders every family.
func (w *Writer) WriteTo(dst io.Writer) (int64, error) {
	cw := &countWriter{w: dst}
	b := bufio.NewWriter(cw)
	for _, f := range w.families {
		b.WriteString("# HELP ")
		b.WriteString(f.name)
		b.WriteByte(' ')
		b.WriteString(helpEscaper.Replace(f.help))
		b.WriteString("\n# TYPE ")
		b.WriteString(f.name)
		b.WriteByte(' ')
		b.WriteString(f.typ)
		b.WriteByte('\n')
		for _, s := range f.samples {
			b.WriteString(f.name)
			if len(s.labels) > 0 {
				b.WriteByte('{')
				for i := 0; i < len(s.labels); i += 2 {
					if i > 0 {
						b.WriteByte(',')
					}
					b.WriteString(s.labels[i])
					b.WriteString(`="`)
					b.WriteString(labelEscaper.Replace(s.labels[i+1]))
					b.WriteByte('"')
				}
				b.WriteByte('}')
			}
			b.WriteByte(' ')
			b.WriteString(formatValue(s.value))
			b.WriteByte('\n')
		}
	}
	err := b.Flush()
	return cw.n, err
}

// The format escapes backslash and newline in HELP text, and also the double
// quote in label values.
var (
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	labelEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
)

func formatValue(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
