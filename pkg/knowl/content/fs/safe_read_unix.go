//go:build linux || darwin

package fs

import (
	"errors"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

func openReadDirectory(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, readOpenError(err)
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

func openReadChild(parent *os.File, name string, directory bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if directory {
		flags |= unix.O_DIRECTORY
	}
	descriptor, err := unix.Openat(int(parent.Fd()), name, flags, 0)
	runtime.KeepAlive(parent)
	if err != nil {
		return nil, readOpenError(err)
	}
	file := os.NewFile(uintptr(descriptor), name)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		_ = file.Close()
		return nil, ErrPathRejected
	}
	return file, nil
}

func readOpenError(err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return errors.Join(ErrPathRejected, err)
	}
	return err
}
