package ipinfo

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

type cacheManifest struct {
	Files     map[string]string `json:"files"`
	UpdatedAt time.Time         `json:"updated_at"`
}

func (m *Manager) manifest() (cacheManifest, error) {
	var v cacheManifest
	f, err := os.Open(filepath.Join(m.options.Directory, "active.json"))
	if err != nil {
		return v, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 16<<10))
	d.DisallowUnknownFields()
	if d.Decode(&v) != nil || d.Decode(new(any)) != io.EOF || v.Files == nil {
		return v, ErrUpdate
	}
	for kind, path := range v.Files {
		if kind != "asn" && kind != "country" && kind != "city" {
			return v, ErrUpdate
		}
		if _, err = cachePath(m.options.Directory, path); err != nil {
			return v, err
		}
		if filepath.Base(path) != kind+".mmdb" {
			return v, ErrUpdate
		}
	}
	return v, nil
}
func openFile(kind, path string, now time.Time) (*database, error) {
	stat, err := os.Stat(path)
	if err != nil || !stat.Mode().IsRegular() || stat.Size() < 1 || stat.Size() > maxRawBytes {
		return nil, ErrUpdate
	}
	r, err := maxminddb.Open(path, maxminddb.DisableStringCache())
	if err != nil {
		return nil, ErrUpdate
	}
	if validate(kind, r, now) != nil || r.Verify() != nil {
		r.Close()
		return nil, ErrUpdate
	}
	return &database{reader: r, info: info(kind, r, stat.Size(), "downloaded", "mmap"), path: path}, nil
}
func (m *Manager) restore() error {
	v, err := m.manifest()
	if err != nil {
		return err
	}
	pending := map[string]*database{}
	accepted := false
	defer func() {
		if !accepted {
			for _, db := range pending {
				db.reader.Close()
			}
		}
	}()
	for kind, path := range v.Files {
		if kind == "city" && !m.options.CityEnabled {
			continue
		}
		full, _ := cachePath(m.options.Directory, path)
		db, err := openFile(kind, full, time.Now().UTC())
		if err != nil {
			return err
		}
		m.mu.RLock()
		old := m.dbs[kind]
		older := old != nil && db.info.BuiltAt.Before(old.info.BuiltAt)
		m.mu.RUnlock()
		if older {
			db.reader.Close()
			continue
		}
		pending[kind] = db
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for kind, db := range pending {
		if old := m.dbs[kind]; old != nil {
			old.reader.Close()
		}
		m.dbs[kind] = db
	}
	if !v.UpdatedAt.IsZero() {
		at := v.UpdatedAt
		m.updated = &at
	}
	accepted = true
	return nil
}
func (m *Manager) needed(edition string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	kinds := []string{"asn", "country"}
	if m.options.CityEnabled {
		kinds = append(kinds, "city")
	}
	out := []string{}
	for _, kind := range kinds {
		if db := m.dbs[kind]; db == nil || db.info.Edition < edition {
			out = append(out, kind)
		}
	}
	return out
}
func download(ctx context.Context, client *http.Client, url, path string) error {
	r, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return ErrUpdate
	}
	r.Header.Set("User-Agent", "Harbormaster-IPDB/1 (+https://github.com/JasperXzy/harbormaster)")
	response, err := client.Do(r)
	if err != nil {
		return ErrUpdate
	}
	defer response.Body.Close()
	const maxPacked = 96 << 20
	if response.StatusCode != 200 || response.ContentLength > maxPacked {
		return ErrUpdate
	}
	z, err := gzip.NewReader(io.LimitReader(response.Body, maxPacked+1))
	if err != nil {
		return ErrUpdate
	}
	defer z.Close()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrUpdate
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(z, maxRawBytes+1))
	if err != nil || n > maxRawBytes || f.Sync() != nil {
		return ErrUpdate
	}
	if err = f.Close(); err != nil {
		return ErrUpdate
	}
	return nil
}
func writeManifest(dir string, v cacheManifest) error {
	f, err := os.CreateTemp(dir, ".active-")
	if err != nil {
		return ErrUpdate
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if json.NewEncoder(f).Encode(v) != nil || f.Sync() != nil || f.Close() != nil {
		return ErrUpdate
	}
	if os.Rename(name, filepath.Join(dir, "active.json")) != nil {
		return ErrUpdate
	}
	return nil
}

// Sync publishes a fully verified set, never a partially downloaded edition.
// It does not downgrade a database when the system clock moves backwards.
func (m *Manager) Sync(ctx context.Context, now time.Time) (err error) {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrUnavailable
	}
	at := now.UTC()
	m.checked = &at
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if err != nil {
			m.lastError = &Failure{Code: ErrUpdate.Error(), Message: ErrUpdate.Error()}
		} else {
			m.lastError = nil
		}
	}()
	edition := now.UTC().Format("2006-01")
	if len(m.needed(edition)) == 0 {
		return nil
	}
	if m.options.Directory == "" || os.MkdirAll(m.options.Directory, 0700) != nil {
		return ErrUpdate
	}
	unlock, err := lockCache(m.options.Directory)
	if errors.Is(err, errBusy) {
		return nil
	}
	if err != nil {
		return ErrUpdate
	}
	defer unlock()
	// A broken cache must not prevent a later verified download replacing it.
	_ = m.restore()
	kinds := m.needed(edition)
	if len(kinds) == 0 {
		return nil
	}
	client := m.options.HTTP
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		defer client.CloseIdleConnections()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp(m.options.Directory, "set-")
	if err != nil {
		return ErrUpdate
	}
	published := false
	defer func() {
		if !published {
			os.RemoveAll(dir)
		}
	}()
	pending := map[string]*database{}
	defer func() {
		if !published {
			for _, db := range pending {
				db.reader.Close()
			}
		}
	}()
	for _, kind := range kinds {
		path := filepath.Join(dir, kind+".mmdb")
		if err = download(ctx, client, sourceURL(kind, edition), path); err != nil {
			return err
		}
		db, err := openFile(kind, path, now)
		if err != nil {
			return err
		}
		if db.info.Edition != edition {
			db.reader.Close()
			return ErrUpdate
		}
		pending[kind] = db
	}
	if ctx.Err() != nil {
		return ErrUpdate
	}
	v, err := m.manifest()
	if err != nil {
		v = cacheManifest{Files: map[string]string{}}
	}
	for kind := range pending {
		v.Files[kind] = filepath.Join(filepath.Base(dir), kind+".mmdb")
	}
	v.UpdatedAt = at
	if err = writeManifest(m.options.Directory, v); err != nil {
		return err
	}
	m.mu.Lock()
	for kind, db := range pending {
		if old := m.dbs[kind]; old != nil {
			old.reader.Close()
		}
		m.dbs[kind] = db
	}
	m.updated = &at
	m.mu.Unlock()
	published = true
	m.cleanup(v)
	return nil
}
func (m *Manager) cleanup(v cacheManifest) {
	keep := map[string]bool{}
	for _, path := range v.Files {
		keep[filepath.Dir(path)] = true
	}
	m.mu.RLock()
	for _, db := range m.dbs {
		if db.path != "" {
			keep[filepath.Base(filepath.Dir(db.path))] = true
		}
	}
	m.mu.RUnlock()
	entries, err := os.ReadDir(m.options.Directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "set-") && !keep[entry.Name()] {
			_ = os.RemoveAll(filepath.Join(m.options.Directory, entry.Name()))
		}
	}
}
func (m *Manager) Run(ctx context.Context) {
	if !m.options.AutoUpdate {
		return
	}
	for ctx.Err() == nil {
		_ = m.Sync(ctx, time.Now().UTC())
		timer := time.NewTimer(24 * time.Hour)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
