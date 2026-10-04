//go:build windows

package sessionlog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func createPrivateFile(path string) (*os.File, error) {
	// Protect the DACL at creation time so transcript bytes are never first
	// written under a broader inherited ACL.
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("get current Windows user for transcript ACL: %w", err)
	}
	userSID := user.User.Sid.String()
	securityDescriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + userSID + ")")
	if err != nil {
		return nil, fmt.Errorf("create private transcript ACL: %w", err)
	}
	securityAttributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: securityDescriptor,
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode transcript path: %w", err)
	}
	handle, err := windows.CreateFile(
		name,
		windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE,
		securityAttributes,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("wrap transcript file handle")
	}
	return file, nil
}

func isFileExistsError(err error) bool {
	return errors.Is(err, fs.ErrExist) || errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS)
}
