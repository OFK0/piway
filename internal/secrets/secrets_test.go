package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSetKeepsCommentsAndOtherLines(t *testing.T) {
	s := &Store{Dir: filepath.Join(t.TempDir(), "secrets")}
	file := s.Path("guestbook")
	os.MkdirAll(s.Dir, 0o700)
	os.WriteFile(file, []byte("# rotated in March\nAPI_KEY=old\n\n# mail\nSMTP_PASS=x\nAPI_KEY=duplicate\n"), 0o644)

	if err := s.Set("guestbook", "API_KEY", "new=with=equals"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("guestbook", "SESSION_SECRET", "abc"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	want := "# rotated in March\nAPI_KEY=new=with=equals\n\n# mail\nSMTP_PASS=x\nSESSION_SECRET=abc\n"
	if string(b) != want {
		t.Errorf("file:\n%s\nwant:\n%s", b, want)
	}
	if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}

	v, err := s.Values("guestbook")
	if err != nil || v["API_KEY"] != "new=with=equals" || v["SMTP_PASS"] != "x" || len(v) != 3 {
		t.Errorf("values %v, %v", v, err)
	}

	if err := s.Remove("guestbook", "SMTP_PASS"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("guestbook", "NEVER_SET"); err != nil {
		t.Errorf("removing a missing secret: %v", err)
	}
	b, _ = os.ReadFile(file)
	if want := "# rotated in March\nAPI_KEY=new=with=equals\n\n# mail\nSESSION_SECRET=abc\n"; string(b) != want {
		t.Errorf("after remove:\n%s", b)
	}
}

func TestSetRejects(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	for _, tc := range []struct{ app, key, value string }{
		{"guestbook", "TWO\nLINES", "x"},
		{"guestbook", "1ST", "x"},
		{"guestbook", "OK", "line one\nline two"},
		{"guestbook", "OK", "carriage\rreturn"},
		{"guestbook", "OK", " padded "},
		{"../etc", "OK", "x"},
	} {
		if err := s.Set(tc.app, tc.key, tc.value); !errors.Is(err, ErrInvalid) {
			t.Errorf("Set(%q, %q, %q) = %v, want ErrInvalid", tc.app, tc.key, tc.value, err)
		}
	}
	if _, err := os.Stat(s.Path("guestbook")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a rejected value created the file")
	}
}

func TestEditRefusesBrokenFile(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	os.WriteFile(s.Path("blog"), []byte("not a pair\n"), 0o600)
	if err := s.Set("blog", "A", "b"); err == nil {
		t.Error("rewrote a file it cannot read")
	}
	if b, _ := os.ReadFile(s.Path("blog")); string(b) != "not a pair\n" {
		t.Errorf("file changed: %q", b)
	}
}

func TestValuesMissingFile(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	v, err := s.Values("nothing")
	if err != nil || len(v) != 0 {
		t.Errorf("Values = %v, %v", v, err)
	}
}

func TestDigest(t *testing.T) {
	a := Digest([]string{"B", "A"}, map[string]string{"A": "1", "B": "2", "UNUSED": "x"})
	if a != Digest([]string{"A", "B"}, map[string]string{"A": "1", "B": "2"}) {
		t.Error("digest depends on order or on unused values")
	}
	if a == Digest([]string{"A", "B"}, map[string]string{"A": "1", "B": "3"}) {
		t.Error("digest ignores a changed value")
	}
	if Digest([]string{"A"}, map[string]string{"A": ""}) == Digest([]string{"A"}, nil) {
		t.Error("an empty value and a missing one look the same")
	}
	if Digest(nil, map[string]string{"A": "1"}) != "" {
		t.Error("no names should give no digest")
	}
}
