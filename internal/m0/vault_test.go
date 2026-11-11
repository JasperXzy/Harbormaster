package m0

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exampleProfile() profile {
	return profile{Name: "example", Site: "china", Regions: []string{"cn-hongkong"}, AccessKeyID: "LTAI-EXAMPLE-NOT-REAL", AccessKeySecret: "synthetic-secret-not-real", SecurityToken: "synthetic-sts-not-real"}
}
func pathsForTest(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("HM_MASTER_KEY", "")
	t.Setenv("HM_MASTER_KEY_FILE", "")
	root := t.TempDir()
	return filepath.Join(root, "vault", "credentials.enc"), filepath.Join(root, "keys", "master.key")
}

func TestEncryptedImportAndReplace(t *testing.T) {
	path, key := pathsForTest(t)
	p := exampleProfile()
	if err := saveProfile(path, key, p, false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{p.AccessKeyID, p.AccessKeySecret, p.SecurityToken, p.Name} {
		if bytes.Contains(b, []byte(value)) {
			t.Fatal("plaintext in vault")
		}
	}
	got, err := getProfile(path, key, p.Name)
	if err != nil || got.AccessKeySecret != p.AccessKeySecret {
		t.Fatal("roundtrip failed")
	}
	changed := p
	changed.AccessKeySecret = "replacement-secret-not-real"
	if saveProfile(path, key, changed, false) == nil {
		t.Fatal("unrequested replacement accepted")
	}
	got, _ = getProfile(path, key, p.Name)
	if got.AccessKeySecret != p.AccessKeySecret {
		t.Fatal("old credentials lost")
	}
	if err = saveProfile(path, key, changed, true); err != nil {
		t.Fatal(err)
	}
	got, _ = getProfile(path, key, p.Name)
	if got.AccessKeySecret != changed.AccessKeySecret {
		t.Fatal("replacement missing")
	}
	keyData, _ := os.ReadFile(key)
	if bytes.Contains(b, keyData) {
		t.Fatal("master key in vault")
	}
	t.Setenv("HM_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	if _, err = getProfile(path, key, p.Name); err == nil {
		t.Fatal("wrong master key accepted")
	}
}

func TestSecretPathBoundaries(t *testing.T) {
	path, _ := pathsForTest(t)
	if saveProfile(path, filepath.Join(filepath.Dir(path), "key"), exampleProfile(), false) == nil {
		t.Fatal("key in vault directory")
	}
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, ".git"), 0700)
	if _, err := externalPath(filepath.Join(root, "data", "key")); err == nil {
		t.Fatal("repository path accepted")
	}
	link := filepath.Join(t.TempDir(), "repo")
	if os.Symlink(root, link) == nil {
		if _, err := externalPath(filepath.Join(link, "key")); err == nil {
			t.Fatal("repository symlink accepted")
		}
	}
}

func TestSetRegionsPreservesCredentials(t *testing.T) {
	path, key := pathsForTest(t)
	p := exampleProfile()
	other := p
	other.Name = "other"
	if err := saveProfile(path, key, p, false); err != nil {
		t.Fatal(err)
	}
	if err := saveProfile(path, key, other, false); err != nil {
		t.Fatal(err)
	}
	regions := []string{"cn-shanghai", "ap-southeast-1", "cn-hongkong"}
	if err := setRegions(path, key, p.Name, regions); err != nil {
		t.Fatal(err)
	}
	got, err := getProfile(path, key, p.Name)
	if err != nil || len(got.Regions) != 3 || got.AccessKeyID != p.AccessKeyID || got.AccessKeySecret != p.AccessKeySecret || got.SecurityToken != p.SecurityToken {
		t.Fatal("credentials changed")
	}
	got, err = getProfile(path, key, other.Name)
	if err != nil || len(got.Regions) != 1 || got.AccessKeySecret != other.AccessKeySecret {
		t.Fatal("other account changed")
	}
	if setRegions(path, key, p.Name, []string{"bad/region"}) == nil {
		t.Fatal("invalid region accepted")
	}
	got, _ = getProfile(path, key, p.Name)
	if len(got.Regions) != 3 {
		t.Fatal("failed update mutated vault")
	}
}

func TestCLIPlanAndNoninteractiveInput(t *testing.T) {
	path, key := pathsForTest(t)
	if err := saveProfile(path, key, exampleProfile(), false); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"plan", "--account", "example", "--vault", path, "--key-file", key}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatal(errOut.String())
	}
	if !strings.Contains(out.String(), `"cloud_requests":0`) {
		t.Fatal("plan format")
	}
	for _, secret := range []string{exampleProfile().AccessKeyID, exampleProfile().AccessKeySecret, exampleProfile().SecurityToken} {
		if strings.Contains(out.String()+errOut.String(), secret) {
			t.Fatal("secret printed")
		}
	}
	out.Reset()
	errOut.Reset()
	if Run(context.Background(), []string{"import", "--account", "example"}, strings.NewReader("secret"), &out, &errOut) != 1 || !strings.Contains(errOut.String(), "interactive_terminal_required") {
		t.Fatal("pipe import accepted")
	}
}
