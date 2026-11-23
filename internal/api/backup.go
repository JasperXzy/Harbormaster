package api

import (
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/JasperXzy/harbormaster/internal/backup"
)

func (p *productionPanel) downloadBackup(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/backups" {
		return false
	}
	if r.Method != "POST" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	var input struct {
		Password string `json:"backup_password"`
	}
	if !readBody(w, r, &input) {
		return true
	}
	password := []byte(input.Password)
	input.Password = ""
	defer clear(password)
	if len(password) < 12 || len(password) > 1024 {
		fieldError(w, "backup_password", "out_of_range")
		return true
	}
	if p.engine == nil || p.engine.Box == nil {
		writeError(w, 503, "backup_unavailable")
		return true
	}
	if !p.backups.TryLock() {
		rateLimited(w, 1)
		return true
	}
	defer p.backups.Unlock()
	if !p.hashing.TryLock() {
		rateLimited(w, 1)
		return true
	}
	// Share the existing expensive-KDF gate with login/setup, releasing it
	// before the client downloads a potentially large file.
	f, err := os.CreateTemp("", "harbormaster-download-*.hmbak")
	if err != nil {
		p.hashing.Unlock()
		writeError(w, 503, "backup_unavailable")
		return true
	}
	defer os.Remove(f.Name())
	defer f.Close()
	err = backup.Export(r.Context(), p.s, p.engine.Box, password, f)
	clear(password)
	p.hashing.Unlock()
	if err == nil {
		err = f.Sync()
	}
	if err != nil {
		writeError(w, 503, "backup_unavailable")
		return true
	}
	// A reset/logout while the snapshot was being prepared must revoke access.
	if _, _, err := p.session(r); err != nil {
		writeError(w, 401, "unauthenticated")
		return true
	}
	info, err := f.Stat()
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		writeError(w, 503, "backup_unavailable")
		return true
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="harbormaster-backup-`+p.now().UTC().Format("20060102T150405Z")+`.hmbak"`)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f) // Content-Length makes interrupted transport detectable.
	return true
}
