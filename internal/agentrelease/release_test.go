package agentrelease

import (
	"crypto/sha256"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var sums strings.Builder
	for _, name := range Files {
		raw := []byte("test artifact " + name)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(raw), name)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
func TestReleaseMustBeCompleteAndChecksummed(t *testing.T) {
	dir := fixture(t)
	m, err := Check(dir)
	if err != nil || len(m.Hashes) != 4 || len(m.Digest) != 64 {
		t.Fatal(m, err)
	}
	if err = os.WriteFile(filepath.Join(dir, Files[0]), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Check(dir); err == nil {
		t.Fatal("corrupt release accepted")
	}
	if _, err = Check(""); err == nil {
		t.Fatal("empty release accepted")
	}
}
func TestDownloadsNeverEscapeReleaseRoot(t *testing.T) {
	dir := fixture(t)
	outside := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(outside, []byte("test secret never real"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../secret.key", "/etc/passwd", "unknown", ""} {
		if Serve(httptest.NewRecorder(), httptest.NewRequest("GET", "https://panel.example/", nil), dir, name) {
			t.Fatal("unlisted path served")
		}
	}
	if err := os.Remove(filepath.Join(dir, "install.sh")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "install.sh")); err != nil {
		t.Fatal(err)
	}
	if Serve(httptest.NewRecorder(), httptest.NewRequest("GET", "https://panel.example/", nil), dir, "install.sh") {
		t.Fatal("symlink escaped release root")
	}
	if _, err := Check(dir); err == nil {
		t.Fatal("symlink release accepted")
	}
}
func TestCommandIsValidShellAndPassesCodeOnStdin(t *testing.T) {
	m, err := Check(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	command := Command("https://panel.example", "test-code-with-'quotes", m)
	if output, err := exec.Command("sh", "-n", "-c", command).CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	if !strings.Contains(command, `| sudo sh "$f" 'https://panel.example'`) || strings.Contains(command, "HM_REG_CODE=") || !strings.Contains(command, m.Digest) || !strings.Contains(command, m.Hashes["install.sh"]) {
		t.Fatal("unsafe command transport")
	}
}
