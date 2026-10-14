package procfs

import (
	"strings"
	"testing"
)

func TestNextLine(t *testing.T) {
	line, rest := NextLine([]byte("a b\nc"))
	if string(line) != "a b" || string(rest) != "c" {
		t.Errorf("NextLine = %q, %q", line, rest)
	}
	line, rest = NextLine([]byte("last"))
	if string(line) != "last" || rest != nil {
		t.Errorf("NextLine without newline = %q, %q", line, rest)
	}
}

func TestNextField(t *testing.T) {
	var got []string
	b := []byte("   8       0 mmcblk0 12  ")
	for {
		var f []byte
		f, b = NextField(b)
		if len(f) == 0 {
			break
		}
		got = append(got, string(f))
	}
	if strings.Join(got, ",") != "8,0,mmcblk0,12" {
		t.Errorf("fields = %q", got)
	}
}

func TestParseUint(t *testing.T) {
	tests := []struct {
		in      string
		want    uint64
		wantErr bool
	}{
		{in: "0", want: 0},
		{in: "210703765", want: 210703765},
		{in: "18446744073709551615", want: 18446744073709551615},
		{in: "18446744073709551616", wantErr: true},
		{in: "", wantErr: true},
		{in: "12a", wantErr: true},
		{in: "-1", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseUint([]byte(tt.in))
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseUint(%q) = %d, %v; want %d, wantErr %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}
