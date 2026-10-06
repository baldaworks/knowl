package fs

import (
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

func operatorFileIdentity(file *os.File) (string, error) {
	var info windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info)
	runtime.KeepAlive(file)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d:%d", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}
