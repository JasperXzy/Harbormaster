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
		if name == "VERSION" {
			raw = []byte("v1.2.0\n")
		}
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
	if err != nil || len(m.Hashes) != len(Files) || len(m.Digest) != 64 {
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
	if !strings.Contains(command, `| "$@" "$f" 'https://panel.example'`) || strings.Contains(command, "HM_REG_CODE=") || !strings.Contains(command, m.Digest) || !strings.Contains(command, m.Hashes["install.sh"]) {
		t.Fatal("unsafe command transport")
	}
}

func TestInstallCommandPrivilegeSelectionAndStdin(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, uid                 string
		hasSudo, wantSudo, failed bool
	}{
		{"root without sudo", "0", false, false, false},
		{"root skips sudo", "0", true, false, false},
		{"user with sudo", "1000", true, true, false},
		{"user without sudo", "1000", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bin, temp := filepath.Join(root, "bin"), filepath.Join(root, "temp")
			for _, dir := range []string{bin, temp} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, tool := range []string{"sh", "cp", "sha256sum", "mktemp", "rm"} {
				path, err := exec.LookPath(tool)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path, filepath.Join(bin, tool)); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("#!"+shell+"\n"+body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(bin, "id"), "printf '%s\\n' '"+tc.uid+"'\n")
			write(filepath.Join(bin, "curl"), `while [ "$#" -gt 0 ]; do
  case "$1" in -o) shift; output=$1;; esac
  shift
done
cp "$HM_TEST_INSTALLER" "$output"
`)
			if tc.hasSudo {
				write(filepath.Join(bin, "sudo"), "printf called > \"$HM_TEST_ROOT/sudo-used\"\nexec \"$@\"\n")
			}
			installer := filepath.Join(root, "installer.sh")
			write(installer, `test "$#" = 2 || exit 91
IFS= read -r code || exit 92
test "$code" = "fixture-code-with-'quotes" || exit 93
test "$1" = 'https://panel.example' || exit 94
test "$2" = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' || exit 95
test "${HM_REG_CODE+x}" != x || exit 96
printf installed > "$HM_TEST_ROOT/installed"
`)
			raw, err := os.ReadFile(installer)
			if err != nil {
				t.Fatal(err)
			}
			m := Manifest{Digest: strings.Repeat("a", 64), Hashes: map[string]string{"install.sh": fmt.Sprintf("%x", sha256.Sum256(raw))}}
			command := exec.Command(shell, "-c", Command("https://panel.example", "fixture-code-with-'quotes", m))
			command.Env = append(os.Environ(), "PATH="+bin, "TMPDIR="+temp, "HM_TEST_ROOT="+root, "HM_TEST_INSTALLER="+installer)
			output, err := command.CombinedOutput()
			if (err != nil) != tc.failed {
				t.Fatalf("unexpected command outcome: %v %s", err, output)
			}
			if tc.failed && !strings.Contains(string(output), "installer_requires_root_or_sudo") {
				t.Fatal("missing actionable privilege error")
			}
			_, installed := os.Stat(filepath.Join(root, "installed"))
			_, sudo := os.Stat(filepath.Join(root, "sudo-used"))
			if (installed == nil) == tc.failed || (sudo == nil) != tc.wantSudo {
				t.Fatal("wrong installer or sudo execution")
			}
		})
	}
}
