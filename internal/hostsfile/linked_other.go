//go:build !unix

package hostsfile

import "os"

func checkLinked(file *os.File) error { _, err := file.Stat(); return err }
