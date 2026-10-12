//go:build unix

package hostsfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteBlock_DetectsUnlinkedInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := checkLinked(f); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := checkLinked(f); !errors.Is(err, ErrUnlinkedInode) {
		t.Fatalf("got %v", err)
	}
}
