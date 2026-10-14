package procfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSeesNewContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if got, err := p.Read(); err != nil || string(got) != "first" {
		t.Fatalf("Read = %q, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("second!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := p.Read(); err != nil || string(got) != "second!" {
		t.Fatalf("Read after rewrite = %q, %v", got, err)
	}
}

func TestReadLargerThanInitialBuffer(t *testing.T) {
	// /proc/stat on many-core machines easily exceeds 4 KiB.
	content := strings.Repeat("0123456789", 2000)
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	got, err := p.Read()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("read %d bytes, want %d", len(got), len(content))
	}
}

func TestReadDoesNotAllocate(t *testing.T) {
	p, err := Open("/proc/self/stat")
	if err != nil {
		t.Skip("procfs not available")
	}
	defer p.Close()

	if _, err := p.Read(); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(100, func() {
		if _, err := p.Read(); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Errorf("Read allocates %v times per call, want 0", allocs)
	}
}
