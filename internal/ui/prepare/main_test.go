package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareDistAndFallback(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "dist")
	dst := filepath.Join(root, "assets")
	if err := prepare(src, dst); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(src, 0755)
	_ = os.WriteFile(filepath.Join(src, "index.html"), []byte("frontend-test-marker"), 0644)
	_ = os.WriteFile(filepath.Join(src, "main.js"), []byte("test"), 0644)
	if err := prepare(src, dst); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dst, "index.html"))
	if string(b) != "frontend-test-marker" {
		t.Fatal("dist not staged")
	}
	_ = os.RemoveAll(src)
	if err := prepare(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "main.js")); !os.IsNotExist(err) {
		t.Fatal("stale asset retained")
	}
	b, _ = os.ReadFile(filepath.Join(dst, "index.html"))
	if !strings.Contains(string(b), "Harbormaster") {
		t.Fatal("no fallback")
	}
	if err := prepare(dst, dst); err == nil {
		t.Fatal("destructive source overlap accepted")
	}
}
