package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/backup"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func TestBackupCLIInputLoggingAndNoOverwrite(t *testing.T) {
	t.Setenv("HM_CONFIG", "")
	t.Setenv("HM_DEV", "false")
	t.Setenv("HM_MASTER_KEY_FILE", "")
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{12}, 32))
	t.Setenv("HM_MASTER_KEY", key)
	dir := t.TempDir()
	var out, logs bytes.Buffer
	ctx := context.Background()
	if code := Server(ctx, []string{"migrate", "--data-dir", dir}, strings.NewReader(""), &out, &logs); code != 0 {
		t.Fatal(code, logs.String())
	}
	path := filepath.Join(t.TempDir(), "test.hmbak")
	password := "cli-backup-password-not-real"
	logs.Reset()
	if code := Server(ctx, []string{"backup", "--data-dir", dir, "--output", path}, strings.NewReader(password+"\n"), &out, &logs); code != 0 {
		t.Fatal(code, logs.String())
	}
	if out.Len() != 0 || strings.Contains(logs.String(), password) || strings.Contains(logs.String(), key) || !strings.Contains(logs.String(), `"cloud_queries":0`) || !strings.Contains(logs.String(), "backup_complete") {
		t.Fatal("CLI logging/input boundary failed")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("backup file not private", info.Mode())
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded bytes.Buffer
	_, err = backup.Unseal(ctx, &decoded, f, []byte(password))
	f.Close()
	if err != nil || !bytes.HasPrefix(decoded.Bytes(), []byte("SQLite format 3")) {
		t.Fatal("CLI produced unusable archive", err)
	}
	for _, args := range [][]string{
		{"backup", "--data-dir", dir, "--output", path},
		{"backup", "--dev", "--data-dir", dir, "--output", path + ".dev"},
		{"backup", "--data-dir", dir, "--output", path + ".bad", "--password", password},
		{"backup", "--data-dir", dir},
	} {
		logs.Reset()
		if code := Server(ctx, args, strings.NewReader(password), &out, &logs); code == 0 || strings.Contains(logs.String(), password) || strings.Contains(logs.String(), key) {
			t.Fatal("invalid invocation accepted or leaked secret", code)
		}
	}
	if _, err := os.Stat(path + ".bad"); !os.IsNotExist(err) {
		t.Fatal("invalid invocation wrote archive", err)
	}
	source := filepath.Join(t.TempDir(), "missing")
	if code := Server(ctx, []string{"backup", "--data-dir", source, "--output", path + ".missing"}, strings.NewReader(password), &out, &logs); code == 0 {
		t.Fatal("missing source accepted")
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatal("backup initialized an absent database")
	}
	s, err := store.Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var jobs int
	if err := s.DB.QueryRow("SELECT count(*) FROM job").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("backup created work", jobs, err)
	}
}
