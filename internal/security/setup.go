package security

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// LoadSetupToken reads an operator-provided bootstrap secret. It is never
// generated into logs, accepted as a CLI argument, or stored in the database raw.
func LoadSetupToken(dataDir string) ([]byte, error) {
	value, file := os.Getenv("HM_SETUP_TOKEN"), os.Getenv("HM_SETUP_TOKEN_FILE")
	if (value == "") == (file == "") {
		return nil, errors.New("provide exactly one of HM_SETUP_TOKEN or HM_SETUP_TOKEN_FILE before initial setup")
	}
	if file != "" {
		f, err := canonical(file)
		if err != nil {
			return nil, errors.New("cannot resolve setup token file")
		}
		d, err := canonical(dataDir)
		if err != nil {
			return nil, errors.New("cannot resolve data directory")
		}
		rel, err := filepath.Rel(d, f)
		if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return nil, errors.New("setup token file must be outside data directory")
		}
		info, err := os.Stat(f)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1024 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			return nil, errors.New("setup token file must be private and regular")
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, errors.New("cannot read setup token file")
		}
		defer clear(b)
		value = strings.TrimSpace(string(b))
	}
	if len(value) < 32 || len(value) > 128 || strings.ContainsAny(value, " \t\r\n") {
		return nil, errors.New("setup token must be 32 to 128 non-whitespace bytes")
	}
	return []byte(value), nil
}
