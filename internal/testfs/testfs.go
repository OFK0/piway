// Package testfs runs a test on a filesystem of a fixed small size, so a
// disk that fills up is real: the kernel answers ENOSPC, nothing is faked.
//
// It needs no root. The test binary starts again inside a user and mount
// namespace (unshare -rm) with a tmpfs mounted over a directory. Where
// unprivileged namespaces are not allowed, the test is skipped; with CI set
// that is a failure, since a gate that skips everything proves nothing.
package testfs

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"testing"
)

const envDir = "PIWAY_TESTFS_DIR"

// Run calls fn with the directory of a tmpfs of at most size bytes. t must
// be a top-level test: it is the one the second run of the binary selects.
func Run(t *testing.T, size int, fn func(t *testing.T, dir string)) {
	t.Helper()
	if dir := os.Getenv(envDir); dir != "" {
		fn(t, dir) // the second run, inside the namespace
		return
	}
	unshare, err := exec.LookPath("unshare")
	if err == nil {
		err = exec.Command(unshare, "-rm", "true").Run()
	}
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("cannot start a mount namespace: %v", err)
		}
		t.Skipf("cannot start a mount namespace: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	mount := t.TempDir()
	cmd := exec.Command(unshare, "-rm", "sh", "-c",
		`mount -t tmpfs -o size="$1" tmpfs "$2" && exec "$3" -test.run "$4" -test.v`,
		"sh", strconv.Itoa(size), mount, exe, "^"+regexp.QuoteMeta(t.Name())+"$")
	cmd.Env = append(os.Environ(), envDir+"="+mount)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("on a %d-byte filesystem: %v\n%s", size, err, out)
	}
}

// Fill writes into dir until the filesystem answers ENOSPC, and returns a
// function that gives the space back.
func Fill(t *testing.T, dir string) (free func()) {
	t.Helper()
	filler := filepath.Join(dir, "filler")
	f, err := os.Create(filler)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 4096)
	for {
		if _, err := f.Write(chunk); err != nil {
			if !errors.Is(err, syscall.ENOSPC) {
				t.Fatal(err)
			}
			break
		}
	}
	f.Close()
	return func() {
		if err := os.Remove(filler); err != nil {
			t.Fatal(err)
		}
	}
}
