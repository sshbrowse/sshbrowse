//go:build !windows

package sessionlog

import (
	"errors"
	"io/fs"
	"os"
)

func createPrivateFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

func isFileExistsError(err error) bool { return errors.Is(err, fs.ErrExist) }
