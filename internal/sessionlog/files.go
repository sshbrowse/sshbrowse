package sessionlog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxNameAttempts = 10000

func createUniqueFile(directory, label string, started time.Time) (*os.File, string, error) {
	baseName := fmt.Sprintf("%s-%s", filenameSlug(label), started.Format("2006-01-02_15-04-05"))
	for suffix := 0; suffix < maxNameAttempts; suffix++ {
		name := baseName
		if suffix > 0 {
			name = fmt.Sprintf("%s-%d", baseName, suffix)
		}
		path := filepath.Join(directory, name+".txt")
		file, err := createPrivateFile(path)
		if err == nil {
			return file, path, nil
		}
		if !isFileExistsError(err) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("could not find an unused session transcript name")
}

func filenameSlug(label string) string {
	var slug strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(label) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' {
			slug.WriteRune(r)
			lastDash = false
		} else if !lastDash && slug.Len() > 0 {
			slug.WriteByte('-')
			lastDash = true
		}
		if slug.Len() >= 120 {
			break
		}
	}
	if slug.Len() == 0 {
		return "local"
	}
	return strings.Trim(slug.String(), "-")
}
