// Package hostsfile manages the avahi-controller-owned block inside /etc/avahi/hosts.
// It owns only the marked section between BEGIN/END markers; all other content is
// preserved verbatim.
package hostsfile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

const (
	beginMarker = "### BEGIN k8s-avahi-controller ###"
	endMarker   = "### END k8s-avahi-controller ###"
)

// ErrMalformedBlock means the managed block cannot be identified safely.
// Refuse to write rather than risk removing unrelated host entries.
var ErrMalformedBlock = errors.New("malformed managed-block markers")

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
// Returns an empty slice if the file does not exist or contains no markers,
// and an error if the markers are malformed or ambiguous.
func (m *Manager) ReadBlock() ([]HostEntry, error) {
	data, err := os.ReadFile(m.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read hosts file: %w", err)
	}

	content := string(data)
	beginIdx, endIdx, err := blockBounds(content)
	if err != nil {
		return nil, err
	}
	if beginIdx == -1 {
		return nil, nil
	}
	lines := strings.Split(content[beginIdx+len(beginMarker):endIdx], "\n")
	var entries []HostEntry

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
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
// A new file is created with 0644 permissions; an existing file keeps its mode.
func (m *Manager) WriteBlock(entries []HostEntry) error {
	file, err := os.OpenFile(m.FilePath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("open hosts file: %w", err)
	}
	defer file.Close()

	old, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read hosts file: %w", err)
	}
	updated, err := replaceBlock(string(old), entries)
	if err != nil {
		return err
	}
	if updated == string(old) {
		return nil
	}

	// The hosts file is a single-file hostPath mount. Replacing it with a
	// renamed temporary file would leave the container bound to the old inode.
	// Stage the complete contents in memory, then overwrite that same inode.
	// In particular, do not truncate before the new contents are written.
	writeErr := writeAllAt(file, []byte(updated))
	if writeErr == nil {
		writeErr = file.Truncate(int64(len(updated)))
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if writeErr != nil {
		// Restore the previous contents when an ordinary write fails. A sudden
		// process or host failure during a write can still leave partial data;
		// the same-inode mount precludes atomic rename-based replacement.
		rollbackErr := writeAllAt(file, old)
		if rollbackErr == nil {
			rollbackErr = file.Truncate(int64(len(old)))
		}
		if rollbackErr == nil {
			rollbackErr = file.Sync()
		}
		if rollbackErr != nil {
			return fmt.Errorf("write hosts file: %w; restore previous contents: %v", writeErr, rollbackErr)
		}
		return fmt.Errorf("write hosts file: %w", writeErr)
	}
	return nil
}

func writeAllAt(file *os.File, data []byte) error {
	for offset := 0; offset < len(data); {
		n, err := file.WriteAt(data[offset:], int64(offset))
		offset += n
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// replaceBlock replaces (or appends) the managed block in the file content string.
func replaceBlock(existing string, entries []HostEntry) (string, error) {
	block := renderBlock(entries)
	beginIdx, endIdx, err := blockBounds(existing)
	if err != nil {
		return "", err
	}
	if beginIdx != -1 {
		after := existing[endIdx+len(endMarker):]
		if block == "" && strings.Trim(after, "\n") == "" {
			// The controller appends one newline after its block. Remove that
			// delimiter when the block is at EOF, keeping any earlier static
			// whitespace and avoiding growth on repeated add/remove cycles.
			after = strings.TrimPrefix(after, "\n")
		}
		return existing[:beginIdx] + block + after, nil
	}

	// No markers found — append block.
	if block == "" {
		return existing, nil
	}
	if existing == "" {
		return block + "\n", nil
	}
	if strings.HasSuffix(existing, "\n\n") {
		return existing + block + "\n", nil
	}
	if strings.HasSuffix(existing, "\n") {
		return existing + "\n" + block + "\n", nil
	}
	return existing + "\n\n" + block + "\n", nil
}

// blockBounds returns the first byte of BEGIN and END for a single valid
// block, or -1/-1 when no block exists. Ambiguous markers require a manual
// repair because the controller cannot distinguish managed from static text.
func blockBounds(content string) (int, int, error) {
	beginCount := strings.Count(content, beginMarker)
	endCount := strings.Count(content, endMarker)
	if beginCount == 0 && endCount == 0 {
		return -1, -1, nil
	}
	beginIdx := strings.Index(content, beginMarker)
	endIdx := strings.Index(content, endMarker)
	if beginCount != 1 || endCount != 1 || beginIdx >= endIdx {
		return -1, -1, ErrMalformedBlock
	}
	return beginIdx, endIdx, nil
}

// renderBlock formats entries as the managed block string, sorted by IP then
// hostname so the output is deterministic even when Services share an IP
// (e.g. MetalLB IP sharing). Returns empty string if entries is nil/empty
// (signals block removal).
func renderBlock(entries []HostEntry) string {
	if len(entries) == 0 {
		return ""
	}

	sorted := make([]HostEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].IP != sorted[j].IP {
			return sorted[i].IP < sorted[j].IP
		}
		return sorted[i].Hostname < sorted[j].Hostname
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
	beginIdx, endIdx, err := blockBounds(content)
	if err != nil {
		return "", err
	}
	if beginIdx == -1 {
		return m.HashBlock(nil), nil
	}

	blockText := content[beginIdx : endIdx+len(endMarker)]
	sum := sha256.Sum256([]byte(blockText))
	return hex.EncodeToString(sum[:]), nil
}
