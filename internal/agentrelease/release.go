// Package agentrelease serves only explicitly named, checksum-verified artifacts.
package agentrelease

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
)

var ErrUnavailable = errors.New("agent_release_unavailable")
var Files = []string{"hm-agent-linux-amd64", "hm-agent-linux-arm64", "hm-agent.service", "install.sh", "VERSION"}

type Manifest struct {
	Digest  string
	Hashes  map[string]string
	Version string
}

func Check(dir string) (Manifest, error) {
	if dir == "" {
		return Manifest{}, ErrUnavailable
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Manifest{}, ErrUnavailable
	}
	defer root.Close()
	manifestFile, err := root.Open("SHA256SUMS")
	if err != nil {
		return Manifest{}, ErrUnavailable
	}
	info, err := manifestFile.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		manifestFile.Close()
		return Manifest{}, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(manifestFile, 4097))
	manifestFile.Close()
	if err != nil || len(raw) > 4096 {
		return Manifest{}, ErrUnavailable
	}
	sum := sha256.Sum256(raw)
	m := Manifest{Digest: hex.EncodeToString(sum[:]), Hashes: map[string]string{}}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || !allowed(fields[1], false) || len(fields[0]) != 64 || m.Hashes[fields[1]] != "" {
			return Manifest{}, ErrUnavailable
		}
		if _, err = hex.DecodeString(fields[0]); err != nil {
			return Manifest{}, ErrUnavailable
		}
		m.Hashes[fields[1]] = fields[0]
	}
	if scanner.Err() != nil || len(m.Hashes) != len(Files) {
		return Manifest{}, ErrUnavailable
	}
	for _, name := range Files {
		f, err := root.Open(name)
		if err != nil {
			return Manifest{}, ErrUnavailable
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 64<<20 {
			f.Close()
			return Manifest{}, ErrUnavailable
		}
		if name == "VERSION" {
			raw, readErr := io.ReadAll(io.LimitReader(f, 66))
			f.Close()
			version, ok := NormalizeVersion(strings.TrimSuffix(string(raw), "\n"))
			sum := sha256.Sum256(raw)
			if readErr != nil || len(raw) > 65 || !ok || hex.EncodeToString(sum[:]) != m.Hashes[name] {
				return Manifest{}, ErrUnavailable
			}
			m.Version = version
			continue
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil || hex.EncodeToString(h.Sum(nil)) != m.Hashes[name] {
			return Manifest{}, ErrUnavailable
		}
	}
	return m, nil
}
func allowed(name string, manifest bool) bool {
	if manifest && name == "SHA256SUMS" {
		return true
	}
	for _, f := range Files {
		if name == f {
			return true
		}
	}
	return false
}
func Serve(w http.ResponseWriter, r *http.Request, dir, name string) bool {
	if !allowed(name, true) || dir == "" {
		return false
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 64<<20 {
		return false
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, info.ModTime(), file)
	return true
}
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// Code is passed through stdin to keep it out of sudo's argument log and the
// installer's process environment. The copied command itself is still secret.
func Command(origin, code string, m Manifest) string {
	return `(umask 077; if [ "$(id -u)" = 0 ]; then set -- sh; elif command -v sudo >/dev/null 2>&1; then set -- sudo sh; else printf '%s\n' 'installer_requires_root_or_sudo' >&2; exit 1; fi; f=$(mktemp) || exit 1; trap 'rm -f "$f"' EXIT; curl --fail --silent --show-error --proto '=https' --tlsv1.2 ` + quote(strings.TrimSuffix(origin, "/")+"/agent/v1/releases/install.sh") + ` -o "$f" && printf '%s  %s\n' ` + quote(m.Hashes["install.sh"]) + ` "$f" | sha256sum -c - >/dev/null && printf '%s\n' ` + quote(code) + ` | "$@" "$f" ` + quote(strings.TrimSuffix(origin, "/")) + ` ` + quote(m.Digest) + `)`
}
