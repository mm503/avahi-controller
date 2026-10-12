//go:build linux

package hostsfile

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Run in a subprocess: the file-size limit and signal disposition are process-wide.
func TestWriteBlock_RestoresOriginalOnWriteError(t *testing.T) {
	if os.Getenv("AVAHI_ROLLBACK_CHILD") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(executable, "-test.run=^TestWriteBlock_RestoresOriginalOnWriteError$", "-test.v")
		cmd.Env = append(os.Environ(), "AVAHI_ROLLBACK_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("rollback subprocess: %v\n%s", err, output)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "hosts")
	original := []byte("127.0.0.1 localhost\n\n" + renderBlock([]HostEntry{{IP: "10.0.0.1", Hostname: "old.local"}}) + "\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}
	limit.Cur = uint64(len(original) + 100)
	signal.Ignore(syscall.SIGXFSZ)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}
	entries := make([]HostEntry, 50)
	for i := range entries {
		entries[i] = HostEntry{IP: "10.0.0.2", Hostname: strings.Repeat("a", 50) + ".local"}
	}
	mgr := &Manager{FilePath: path}
	if err := mgr.WriteBlock(entries); !errors.Is(err, syscall.EFBIG) {
		t.Fatalf("expected partial EFBIG write, got %v", err)
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, original) {
		t.Fatalf("original not restored: got %d bytes, want %d", len(actual), len(original))
	}
	got, err := mgr.ReadBlock()
	if err != nil || len(got) != 1 || got[0].Hostname != "old.local" {
		t.Fatalf("restored block unreadable: %v %v", got, err)
	}
}
