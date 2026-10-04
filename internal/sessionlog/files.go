package sessionlog

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func uniqueNamePrefix(label string, started time.Time) (string, error) {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("create unique session log name: %w", err)
	}
	slug := filenameSlug(label)
	if slug == "" {
		slug = "session"
	}
	return fmt.Sprintf("session-%s-%s-%s", slug, started.Format("20060102T150405.000000000Z"), hex.EncodeToString(nonce[:])), nil
}

func filenameSlug(label string) string {
	var slug strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(label) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			slug.WriteRune(r)
			lastDash = false
		} else if !lastDash && slug.Len() > 0 {
			slug.WriteByte('-')
			lastDash = true
		}
		if slug.Len() >= 48 {
			break
		}
	}
	return strings.Trim(slug.String(), "-")
}

func segmentPath(directory, prefix string, segment int) string {
	return filepath.Join(directory, fmt.Sprintf("%s-%03d.txt", prefix, segment))
}

func createUniqueSegment(directory, prefix string, segment int) (*os.File, string, string, error) {
	for attempt := 0; attempt < 10; attempt++ {
		path := segmentPath(directory, prefix, segment)
		file, err := createPrivateFile(path)
		if err == nil {
			return file, path, prefix, nil
		}
		if !isFileExistsError(err) {
			return nil, "", prefix, err
		}
		var nonce [8]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, "", prefix, fmt.Errorf("retry unique session log name: %w", err)
		}
		prefix = fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(nonce[:]))
	}
	return nil, "", prefix, errors.New("could not find an unused session transcript name")
}
