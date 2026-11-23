package security

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSetupTokenSources(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	token := strings.Repeat("t", 48)
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HM_SETUP_TOKEN", "")
	t.Setenv("HM_SETUP_TOKEN_FILE", "")
	if _, err := LoadSetupToken(data); err == nil {
		t.Fatal("missing credential accepted")
	}
	t.Setenv("HM_SETUP_TOKEN", token)
	if got, err := LoadSetupToken(data); err != nil || string(got) != token {
		t.Fatal("valid environment rejected")
	}
	file := filepath.Join(root, "setup-token")
	if err := os.WriteFile(file, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HM_SETUP_TOKEN_FILE", file)
	if _, err := LoadSetupToken(data); err == nil {
		t.Fatal("ambiguous source accepted")
	}
	t.Setenv("HM_SETUP_TOKEN", "")
	if got, err := LoadSetupToken(data); err != nil || string(got) != token {
		t.Fatal("valid file rejected")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(file, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSetupToken(data); err == nil {
			t.Fatal("public bootstrap file accepted")
		}
		if err := os.Chmod(file, 0600); err != nil {
			t.Fatal(err)
		}
	}
	inside := filepath.Join(data, "setup-token")
	if err := os.WriteFile(inside, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HM_SETUP_TOKEN_FILE", inside)
	if _, err := LoadSetupToken(data); err == nil {
		t.Fatal("bootstrap file inside data accepted")
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "linked-token")
		if err := os.Symlink(inside, link); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HM_SETUP_TOKEN_FILE", link)
		if _, err := LoadSetupToken(data); err == nil {
			t.Fatal("symlink bypassed data boundary")
		}
	}
	t.Setenv("HM_SETUP_TOKEN_FILE", "")
	t.Setenv("HM_SETUP_TOKEN", "short")
	if _, err := LoadSetupToken(data); err == nil {
		t.Fatal("short token accepted")
	}
}
