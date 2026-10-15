package setup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRelease is a release whose piway binary is a script printing its
// version, served like GitHub's /releases/... URLs.
func fakeRelease(t *testing.T, tamper bool) *httptest.Server {
	t.Helper()
	var tgz bytes.Buffer
	gz := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(gz)
	// "setup -h" succeeds, like a release that has piway setup.
	bin := []byte("#!/bin/sh\n[ \"$1\" = setup ] && exit 0\necho 'piway v9.9.9 (test)'\n")
	if err := tw.WriteHeader(&tar.Header{Name: "piway_linux_arm64/piway", Mode: 0o755, Size: int64(len(bin))}); err != nil {
		t.Fatal(err)
	}
	tw.Write(bin)
	tw.Close()
	gz.Close()
	archive := tgz.Bytes()
	sum := sha256.Sum256(archive)
	if tamper {
		archive = append(archive, 0) // the checksum no longer matches
	}
	sums := fmt.Sprintf("%x  piway_linux_arm64.tar.gz\n", sum)

	mux := http.NewServeMux()
	for _, dir := range []string{"/latest/download/", "/download/v1.2.3/"} {
		mux.HandleFunc(dir+"piway_linux_arm64.tar.gz", func(w http.ResponseWriter, _ *http.Request) { w.Write(archive) })
		mux.HandleFunc(dir+"checksums.txt", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, sums) })
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func runInstall(t *testing.T, srv *httptest.Server, arch string, args ...string) (string, string, error) {
	t.Helper()
	for _, tool := range []string{"sh", "curl", "sha256sum", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	home := t.TempDir()
	cmd := exec.Command("sh", append([]string{filepath.Join("..", "..", "install.sh")}, args...)...)
	cmd.Env = append(os.Environ(), "HOME="+home, "PIWAY_RELEASES="+srv.URL, "PIWAY_BIN_DIR="+filepath.Join(home, "bin"), "PIWAY_ARCH="+arch)
	cmd.Stdin = strings.NewReader("")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), filepath.Join(home, "bin", "piway"), err
}

func TestInstallScript(t *testing.T) {
	srv := fakeRelease(t, false)

	out, bin, err := runInstall(t, srv, "aarch64", "--no-setup")
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Installed piway v9.9.9 (test) at "+bin) {
		t.Errorf("output:\n%s", out)
	}
	if st, err := os.Stat(bin); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("binary: %v %v", st, err)
	}

	if out, _, err := runInstall(t, srv, "aarch64", "--version", "v1.2.3", "--no-setup"); err != nil {
		t.Errorf("--version: %v\n%s", err, out)
	}
	if out, _, err := runInstall(t, srv, "aarch64", "--version", "v0.0.1", "--no-setup"); err == nil || !strings.Contains(out, "could not download") {
		t.Errorf("missing release: %v\n%s", err, out)
	}
}

func TestInstallScriptRefuses(t *testing.T) {
	out, bin, err := runInstall(t, fakeRelease(t, true), "aarch64", "--no-setup")
	if err == nil || !strings.Contains(out, "checksum mismatch") {
		t.Errorf("tampered release: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(bin); statErr == nil {
		t.Error("a tampered release was installed")
	}

	srv := fakeRelease(t, false)
	if out, _, err := runInstall(t, srv, "armv7l", "--no-setup"); err == nil || !strings.Contains(out, "64-bit") {
		t.Errorf("32-bit: %v\n%s", err, out)
	}
	if out, _, err := runInstall(t, srv, "x86_64", "--no-setup"); err == nil || !strings.Contains(out, "x86_64") {
		t.Errorf("x86_64: %v\n%s", err, out)
	}
}

// Without --no-setup and without a terminal, the script installs and says
// how to continue instead of guessing answers.
func TestInstallScriptWithoutTerminal(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exec 9</dev/tty") // is there a terminal at all?
	if cmd.Run() == nil {
		t.Skip("a terminal is attached; setup would start")
	}
	out, _, err := runInstall(t, fakeRelease(t, false), "aarch64")
	if err != nil || !strings.Contains(out, "No terminal to ask questions on") {
		t.Errorf("%v\n%s", err, out)
	}
}
