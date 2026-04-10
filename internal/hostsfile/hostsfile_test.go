package hostsfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- helpers ---

func newManager(t *testing.T, content string) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	return &Manager{FilePath: path}, path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	return string(b)
}

// --- ReadBlock tests ---

func TestReadBlock_FileNotExist(t *testing.T) {
	m := &Manager{FilePath: "/nonexistent/path/hosts"}
	entries, err := m.ReadBlock()
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty entries, got %v", entries)
	}
}

func TestReadBlock_EmptyFile(t *testing.T) {
	m, _ := newManager(t, "")
	entries, err := m.ReadBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty entries, got %v", entries)
	}
}

func TestReadBlock_NoMarkers(t *testing.T) {
	m, _ := newManager(t, "192.168.1.1 static.local\n")
	entries, err := m.ReadBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty entries (no markers), got %v", entries)
	}
}

func TestReadBlock_WithBlock(t *testing.T) {
	content := "# static\n" +
		beginMarker + "\n" +
		"192.168.1.100 myapp.local\n" +
		"192.168.1.101 otherapp.local\n" +
		endMarker + "\n"

	m, _ := newManager(t, content)
	entries, err := m.ReadBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(entries), entries)
	}
	if entries[0].IP != "192.168.1.100" || entries[0].Hostname != "myapp.local" {
		t.Errorf("entry[0] mismatch: %+v", entries[0])
	}
	if entries[1].IP != "192.168.1.101" || entries[1].Hostname != "otherapp.local" {
		t.Errorf("entry[1] mismatch: %+v", entries[1])
	}
}

func TestReadBlock_IgnoresCommentsInsideBlock(t *testing.T) {
	content := beginMarker + "\n" +
		"# this is a comment\n" +
		"10.0.0.1 svc.local\n" +
		endMarker + "\n"

	m, _ := newManager(t, content)
	entries, err := m.ReadBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
}

// --- WriteBlock tests ---

func TestWriteBlock_AppendWhenNoMarkers(t *testing.T) {
	m, path := newManager(t, "# static entry\n192.168.0.1 router.local\n")

	err := m.WriteBlock([]HostEntry{
		{IP: "10.0.0.1", Hostname: "svc.local"},
	})
	if err != nil {
		t.Fatal(err)
	}

	out := readFile(t, path)
	if !strings.Contains(out, "# static entry") {
		t.Error("static content was lost")
	}
	if !strings.Contains(out, "192.168.0.1 router.local") {
		t.Error("static host entry was lost")
	}
	if !strings.Contains(out, beginMarker) {
		t.Error("begin marker missing")
	}
	if !strings.Contains(out, "10.0.0.1 svc.local") {
		t.Error("managed entry missing")
	}
	if !strings.Contains(out, endMarker) {
		t.Error("end marker missing")
	}
}

func TestWriteBlock_ReplaceExisting(t *testing.T) {
	content := "# top\n" +
		beginMarker + "\n" +
		"192.168.1.1 old.local\n" +
		endMarker + "\n" +
		"# bottom\n"

	m, path := newManager(t, content)

	err := m.WriteBlock([]HostEntry{
		{IP: "10.0.0.1", Hostname: "new.local"},
	})
	if err != nil {
		t.Fatal(err)
	}

	out := readFile(t, path)
	if strings.Contains(out, "192.168.1.1 old.local") {
		t.Error("old entry should be gone")
	}
	if !strings.Contains(out, "10.0.0.1 new.local") {
		t.Error("new entry missing")
	}
	if !strings.Contains(out, "# top") {
		t.Error("content above block was lost")
	}
	if !strings.Contains(out, "# bottom") {
		t.Error("content below block was lost")
	}
}

func TestWriteBlock_RemovesBlockWhenEmpty(t *testing.T) {
	content := "# static\n" +
		beginMarker + "\n" +
		"10.0.0.1 svc.local\n" +
		endMarker + "\n"

	m, path := newManager(t, content)

	err := m.WriteBlock(nil)
	if err != nil {
		t.Fatal(err)
	}

	out := readFile(t, path)
	if strings.Contains(out, beginMarker) {
		t.Error("begin marker should be removed")
	}
	if strings.Contains(out, endMarker) {
		t.Error("end marker should be removed")
	}
	if !strings.Contains(out, "# static") {
		t.Error("static content was lost")
	}
}

func TestWriteBlock_EmptyFileNoMarkers(t *testing.T) {
	m, path := newManager(t, "")

	err := m.WriteBlock([]HostEntry{
		{IP: "10.0.0.1", Hostname: "svc.local"},
	})
	if err != nil {
		t.Fatal(err)
	}

	out := readFile(t, path)
	if !strings.Contains(out, beginMarker) {
		t.Error("begin marker missing")
	}
	if !strings.Contains(out, "10.0.0.1 svc.local") {
		t.Error("entry missing")
	}
}

func TestWriteBlock_FileNotExistYet(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{FilePath: filepath.Join(dir, "hosts")}

	err := m.WriteBlock([]HostEntry{
		{IP: "10.0.0.1", Hostname: "svc.local"},
	})
	if err != nil {
		t.Fatal(err)
	}

	entries, err := m.ReadBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].IP != "10.0.0.1" {
		t.Errorf("unexpected entries: %v", entries)
	}
}

func TestWriteBlock_SortsEntriesByIP(t *testing.T) {
	m, path := newManager(t, "")

	err := m.WriteBlock([]HostEntry{
		{IP: "10.0.0.3", Hostname: "c.local"},
		{IP: "10.0.0.1", Hostname: "a.local"},
		{IP: "10.0.0.2", Hostname: "b.local"},
	})
	if err != nil {
		t.Fatal(err)
	}

	out := readFile(t, path)
	idx1 := strings.Index(out, "10.0.0.1")
	idx2 := strings.Index(out, "10.0.0.2")
	idx3 := strings.Index(out, "10.0.0.3")
	if !(idx1 < idx2 && idx2 < idx3) {
		t.Errorf("entries not sorted by IP:\n%s", out)
	}
}

func TestWriteBlock_BlankLineBeforeBlock(t *testing.T) {
	m, path := newManager(t, "# static\n")

	err := m.WriteBlock([]HostEntry{{IP: "10.0.0.1", Hostname: "svc.local"}})
	if err != nil {
		t.Fatal(err)
	}

	out := readFile(t, path)
	if !strings.Contains(out, "# static\n\n"+beginMarker) {
		t.Errorf("expected blank line before block:\n%s", out)
	}
}

// --- HashBlock tests ---

func TestHashBlock_Deterministic(t *testing.T) {
	m := &Manager{}

	entries1 := []HostEntry{
		{IP: "10.0.0.2", Hostname: "b.local"},
		{IP: "10.0.0.1", Hostname: "a.local"},
	}
	entries2 := []HostEntry{
		{IP: "10.0.0.1", Hostname: "a.local"},
		{IP: "10.0.0.2", Hostname: "b.local"},
	}

	if m.HashBlock(entries1) != m.HashBlock(entries2) {
		t.Error("hash not deterministic across different input orders")
	}
}

func TestHashBlock_DifferentEntries(t *testing.T) {
	m := &Manager{}
	h1 := m.HashBlock([]HostEntry{{IP: "10.0.0.1", Hostname: "a.local"}})
	h2 := m.HashBlock([]HostEntry{{IP: "10.0.0.2", Hostname: "b.local"}})
	if h1 == h2 {
		t.Error("different entries should produce different hashes")
	}
}

func TestHashBlock_EmptyMatchesEmpty(t *testing.T) {
	m := &Manager{}
	if m.HashBlock(nil) != m.HashBlock([]HostEntry{}) {
		t.Error("nil and empty should hash the same")
	}
}

func TestHashCurrentBlock_MatchesWritten(t *testing.T) {
	m, _ := newManager(t, "")

	entries := []HostEntry{
		{IP: "10.0.0.1", Hostname: "svc.local"},
	}

	if err := m.WriteBlock(entries); err != nil {
		t.Fatal(err)
	}

	want := m.HashBlock(entries)
	got, err := m.HashCurrentBlock()
	if err != nil {
		t.Fatal(err)
	}
	if want != got {
		t.Errorf("hash mismatch after write: want %s got %s", want, got)
	}
}

// --- roundtrip ---

func TestRoundtrip_WriteAndReadBack(t *testing.T) {
	m, _ := newManager(t, "")

	original := []HostEntry{
		{IP: "10.0.0.1", Hostname: "alpha.local"},
		{IP: "10.0.0.3", Hostname: "gamma.local"},
		{IP: "10.0.0.2", Hostname: "beta.local"},
	}

	if err := m.WriteBlock(original); err != nil {
		t.Fatal(err)
	}

	got, err := m.ReadBlock()
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	// Sorted by IP after write
	if got[0].IP != "10.0.0.1" || got[1].IP != "10.0.0.2" || got[2].IP != "10.0.0.3" {
		t.Errorf("unexpected order: %v", got)
	}
}
