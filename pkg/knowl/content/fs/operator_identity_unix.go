//go:build linux || darwin

package fs

import (
	"fmt"
	"os"
	"syscall"
)

func operatorFileIdentity(file *os.File) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ErrPathRejected
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}
