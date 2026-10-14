package prom

import (
	"math"
	"strings"
	"testing"
)

func render(t *testing.T, w *Writer) string {
	t.Helper()
	var b strings.Builder
	n, err := w.WriteTo(&b)
	if err != nil {
		t.Fatal(err)
	}
	if int(n) != b.Len() {
		t.Errorf("WriteTo returned %d, wrote %d bytes", n, b.Len())
	}
	return b.String()
}

func TestWriterGroupsFamilies(t *testing.T) {
	var w Writer
	w.Add("a_total", Counter, "A.", 1, "x", "1")
	w.Add("b", Gauge, "B.", 2)
	w.Add("a_total", Counter, "ignored", 3, "x", "2") // joins the first a_total
	want := `# HELP a_total A.
# TYPE a_total counter
a_total{x="1"} 1
a_total{x="2"} 3
# HELP b B.
# TYPE b gauge
b 2
`
	if got := render(t, &w); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestWriterEscapes(t *testing.T) {
	var w Writer
	w.Add("m", Gauge, "line one\nback\\slash", 1, "path", `C:\a "b"`+"\nc", "other", "plain")
	want := `# HELP m line one\nback\\slash
# TYPE m gauge
m{path="C:\\a \"b\"\nc",other="plain"} 1
`
	if got := render(t, &w); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestFormatValue(t *testing.T) {
	for v, want := range map[float64]string{
		0: "0", 1.5: "1.5", 1e21: "1e+21", 123456789: "1.23456789e+08",
		math.Inf(1): "+Inf", math.Inf(-1): "-Inf",
	} {
		if got := formatValue(v); got != want {
			t.Errorf("formatValue(%v) = %q, want %q", v, got, want)
		}
	}
	if got := formatValue(math.NaN()); got != "NaN" {
		t.Errorf("formatValue(NaN) = %q", got)
	}
}

func TestWriterRejectsOddLabels(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Add with an odd number of label strings did not panic")
		}
	}()
	var w Writer
	w.Add("m", Gauge, "M.", 1, "only-a-name")
}
