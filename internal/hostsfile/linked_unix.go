//go:build unix

package hostsfile

import (
	"os"
	"syscall"
)

func checkLinked(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink == 0 {
		return ErrUnlinkedInode
	}
	return nil
}
