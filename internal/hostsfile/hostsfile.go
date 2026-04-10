// Package hostsfile manages the avahi-controller-owned block inside /etc/avahi/hosts.
// It owns only the marked section between BEGIN/END markers; all other content is
// preserved verbatim.
package hostsfile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	beginMarker = "### BEGIN k8s-avahi-controller ###"
	endMarker   = "### END k8s-avahi-controller ###"
)

// HostEntry represents one line in the managed block.
type HostEntry struct {
	IP       string
	Hostname string
}

// Manager owns the hosts file path and all read/write operations on the managed block.
type Manager struct {
	FilePath string
}

// ReadBlock reads the current file and returns the entries found inside the managed block.
// Returns an empty slice if the file does not exist or contains no markers.
func (m *Manager) ReadBlock() ([]HostEntry, error) {
	data, err := os.ReadFile(m.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read hosts file: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	inBlock := false
	var entries []HostEntry

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == beginMarker {
			inBlock = true
			continue
		}
		if line == endMarker {
			break
		}
		if !inBlock || line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) >= 2 {
			entries = append(entries, HostEntry{IP: fields[0], Hostname: fields[1]})
		}
	}

	return entries, nil
}

// WriteBlock replaces the managed block in the hosts file with the given entries.
// If entries is nil or empty, the block (including markers) is removed entirely.
// Static content outside the markers is preserved.
// The file is written with 0644 permissions so avahi-daemon (avahi user) can read it.
func (m *Manager) WriteBlock(entries []HostEntry) error {
	existing := ""
	data, err := os.ReadFile(m.FilePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read hosts file: %w", err)
	}
	if err == nil {
		existing = string(data)
	}

	if err := os.WriteFile(m.FilePath, []byte(replaceBlock(existing, entries)), 0644); err != nil {
		return fmt.Errorf("write hosts file: %w", err)
	}
	return nil
}

// replaceBlock replaces (or appends) the managed block in the file content string.
func replaceBlock(existing string, entries []HostEntry) string {
	block := renderBlock(entries)

	beginIdx := strings.Index(existing, beginMarker)
	endIdx := strings.Index(existing, endMarker)

	if beginIdx != -1 && endIdx != -1 && beginIdx < endIdx {
		before := strings.TrimRight(existing[:beginIdx], "\n")
		after := strings.TrimLeft(existing[endIdx+len(endMarker):], "\n")

		if block == "" {
			if before == "" {
				return strings.TrimLeft(after, "\n")
			}
			if after == "" {
				return before + "\n"
			}
			return before + "\n" + after + "\n"
		}

		parts := []string{}
		if before != "" {
			parts = append(parts, before)
		}
		parts = append(parts, block)
		if after != "" {
			parts = append(parts, strings.TrimLeft(after, "\n"))
		}
		return strings.Join(parts, "\n\n") + "\n"
	}

	// No markers found — append block.
	if block == "" {
		return existing
	}
	trimmed := strings.TrimRight(existing, "\n")
	if trimmed == "" {
		return block + "\n"
	}
	return trimmed + "\n\n" + block + "\n"
}

// renderBlock formats entries as the managed block string, sorted by IP.
// Returns empty string if entries is nil/empty (signals block removal).
func renderBlock(entries []HostEntry) string {
	if len(entries) == 0 {
		return ""
	}

	sorted := make([]HostEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].IP < sorted[j].IP
	})

	var sb strings.Builder
	sb.WriteString(beginMarker + "\n")
	sb.WriteString("# Managed by avahi-controller. Do not edit between these markers.\n")
	for _, e := range sorted {
		sb.WriteString(e.IP + " " + e.Hostname + "\n")
	}
	sb.WriteString(endMarker)
	return sb.String()
}

// HashBlock returns a deterministic SHA-256 hex digest of the sorted, rendered block.
func (m *Manager) HashBlock(entries []HostEntry) string {
	sum := sha256.Sum256([]byte(renderBlock(entries)))
	return hex.EncodeToString(sum[:])
}

// HashCurrentBlock reads the raw block text from the file and hashes it directly.
// Returns the hash of an empty block if the file has no managed section.
func (m *Manager) HashCurrentBlock() (string, error) {
	data, err := os.ReadFile(m.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return m.HashBlock(nil), nil
		}
		return "", fmt.Errorf("read hosts file: %w", err)
	}

	content := string(data)
	beginIdx := strings.Index(content, beginMarker)
	endIdx := strings.Index(content, endMarker)
	if beginIdx == -1 || endIdx == -1 || beginIdx >= endIdx {
		return m.HashBlock(nil), nil
	}

	blockText := content[beginIdx : endIdx+len(endMarker)]
	sum := sha256.Sum256([]byte(blockText))
	return hex.EncodeToString(sum[:]), nil
}
