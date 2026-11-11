package agentrelease

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the real shell control flow against a temporary filesystem and
// explicit command doubles. No system user, service, firewall or key is touched.
func TestInstallerRestoresFilesButRetainsStateAfterFailedUpgrade(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "commands")
	release := filepath.Join(root, "release")
	for _, dir := range []string{bin, release, filepath.Join(root, "var/tmp"), filepath.Join(root, "etc/systemd/system")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0700); err != nil {
			t.Fatal(err)
		}
	}
	script, err := os.ReadFile("../../deploy/agent/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	rewritten := strings.NewReplacer("/etc/harbormaster", root+"/etc/harbormaster", "/var/lib/harbormaster-agent", root+"/var/lib/harbormaster-agent", "/usr/local/lib/harbormaster", root+"/usr/local/lib/harbormaster", "/etc/systemd/system", root+"/etc/systemd/system", "/var/tmp", root+"/var/tmp").Replace(string(script))
	installer := filepath.Join(root, "install.sh")
	write(installer, rewritten)
	doubles := map[string]string{
		"id":      `printf '0\n'`,
		"uname":   `case "$1" in -s) printf 'Linux\n';; -m) printf 'x86_64\n';; esac`,
		"getent":  `test -f "$HM_TEST_ROOT/user-created"`,
		"useradd": `touch "$HM_TEST_ROOT/user-created"`,
		"chown":   `exit 0`,
		"openssl": `printf 'dGVzdC1vbmx5LWtleS1ub3QtYS1yZWFsLXNlY3JldA==\n'`,
		"ip":      `printf 'default via 10.0.0.1 dev eth0\n'`,
		"curl": `while [ "$#" -gt 0 ]; do case "$1" in https://*) url=$1;; -o) shift; output=$1;; esac; shift; done
cp "$HM_TEST_RELEASE/${url##*/}" "$output"`,
		"install": `if [ "$1" = -d ]; then
shift; test "$1" = -m || exit 1; mode=$2; shift 2
if [ "$1" = -o ]; then shift 4; fi
exec /usr/bin/install -d -m "$mode" "$@"
fi
exec /usr/bin/install "$@"`,
		"systemctl": `case "$1" in
--version) printf 'systemd 252\n';;
is-active) test -f "$HM_TEST_ROOT/service-active";;
is-enabled) test -f "$HM_TEST_ROOT/service-enabled";;
stop) rm -f "$HM_TEST_ROOT/service-active";;
start) touch "$HM_TEST_ROOT/service-active";;
enable) touch "$HM_TEST_ROOT/service-enabled"; if [ "${2:-}" = --now ]; then touch "$HM_TEST_ROOT/service-active"; fi;;
disable) rm -f "$HM_TEST_ROOT/service-enabled";;
daemon-reload) :;;
*) exit 1;; esac`,
		"sleep": `exit 0`,
	}
	for name, body := range doubles {
		write(filepath.Join(bin, name), "#!/bin/sh\nset -eu\n"+body+"\n")
	}
	checksum, err := exec.LookPath("sha256sum")
	wrapper := ""
	if err == nil {
		wrapper = "exec " + quote(checksum) + ` "$@"`
	} else {
		checksum, err = exec.LookPath("shasum")
		if err != nil {
			t.Fatal(err)
		}
		wrapper = "exec " + quote(checksum) + ` -a 256 "$@"`
	}
	write(filepath.Join(bin, "sha256sum"), "#!/bin/sh\n"+wrapper+"\n")
	artifact := `#!/bin/sh
set -eu
case "$1" in
version) exit 0;;
register)
IFS= read -r code
test "$code" = "hm_reg_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || exit 9
printf 'registration-attempt\n' >> "$HM_TEST_ROOT/var/lib/harbormaster-agent/preserved-state"
if [ "${HM_TEST_FAIL_REGISTER:-0}" = 1 ]; then exit 8; fi;;
remove-counters) :;;
*) exit 1;;
esac
`
	write(filepath.Join(release, "hm-agent-linux-amd64"), artifact)
	write(filepath.Join(release, "hm-agent-linux-arm64"), artifact)
	write(filepath.Join(release, "hm-agent.service"), "# test unit\n")
	write(filepath.Join(release, "install.sh"), string(script))
	manifest := func() string {
		var b strings.Builder
		for _, name := range Files {
			raw, err := os.ReadFile(filepath.Join(release, name))
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%x  %s\n", sha256.Sum256(raw), name)
		}
		write(filepath.Join(release, "SHA256SUMS"), b.String())
		return fmt.Sprintf("%x", sha256.Sum256([]byte(b.String())))
	}
	run := func(fail bool) ([]byte, error) {
		cmd := exec.Command("sh", installer, "https://panel.example", manifest())
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "HM_TEST_ROOT="+root, "HM_TEST_RELEASE="+release)
		if fail {
			cmd.Env = append(cmd.Env, "HM_TEST_FAIL_REGISTER=1")
		}
		cmd.Stdin = strings.NewReader("hm_reg_" + strings.Repeat("a", 64) + "\n")
		return cmd.CombinedOutput()
	}
	if output, err := run(false); err != nil {
		t.Fatal(err, string(output))
	}
	installed := filepath.Join(root, "usr/local/lib/harbormaster/hm-agent")
	first, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(release, "hm-agent-linux-amd64"), artifact+"# new release\n")
	if output, err := run(true); err == nil {
		t.Fatal("failed registration reported success", string(output))
	} else if bytes.Contains(output, []byte("hm_reg_")) {
		t.Fatal("installer logged code")
	}
	restored, err := os.ReadFile(installed)
	if err != nil || !bytes.Equal(first, restored) {
		t.Fatal("previous executable not restored", err)
	}
	state, err := os.ReadFile(filepath.Join(root, "var/lib/harbormaster-agent/preserved-state"))
	if err != nil || strings.Count(string(state), "registration-attempt") != 2 {
		t.Fatal("rollback erased remotely committed registration state")
	}
	if _, err = os.Stat(filepath.Join(root, "service-active")); err != nil {
		t.Fatal("previous active service not restarted")
	}
	if _, err = os.Stat(filepath.Join(root, "etc/harbormaster/agent.key")); err != nil {
		t.Fatal("rollback erased key")
	}
}
