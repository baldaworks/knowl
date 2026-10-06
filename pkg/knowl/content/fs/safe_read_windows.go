package fs

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openReadDirectory(path string) (*os.File, error) {
	name := `\??\` + path
	if strings.HasPrefix(path, `\\`) {
		name = `\??\UNC\` + strings.TrimPrefix(path, `\\`)
	}
	return openReadHandle(0, name, true)
}

func openReadChild(parent *os.File, name string, directory bool) (*os.File, error) {
	file, err := openReadHandle(windows.Handle(parent.Fd()), name, directory)
	runtime.KeepAlive(parent)
	return file, err
}

func openReadHandle(parent windows.Handle, name string, directory bool) (*os.File, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, ErrPathRejected
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: parent, ObjectName: objectName, Attributes: windows.OBJ_DONT_REPARSE | windows.OBJ_CASE_INSENSITIVE}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if directory {
		options |= windows.FILE_DIRECTORY_FILE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ, &attributes, &status, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, options, 0, 0)
	if err != nil {
		if errors.Is(err, windows.STATUS_REPARSE_POINT_ENCOUNTERED) || errors.Is(err, windows.STATUS_NOT_A_DIRECTORY) || errors.Is(err, windows.STATUS_FILE_IS_A_DIRECTORY) {
			return nil, errors.Join(ErrPathRejected, err)
		}
		var ntStatus windows.NTStatus
		if errors.As(err, &ntStatus) {
			return nil, ntStatus.Errno()
		}
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	kind, err := windows.GetFileType(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || kind != windows.FILE_TYPE_DISK {
		_ = windows.CloseHandle(handle)
		return nil, ErrPathRejected
	}
	return os.NewFile(uintptr(handle), name), nil
}
