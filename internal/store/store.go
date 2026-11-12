package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct{ DB *sql.DB }

func Open(ctx context.Context, dir string, dev bool) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("cannot create data directory")
	}
	name, err := filepath.Abs(filepath.Join(dir, "harbormaster.db"))
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("cannot open database file")
	}
	_ = f.Close()
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(name)}
	q := u.Query()
	for _, v := range []string{"foreign_keys(1)", "busy_timeout(5000)", "journal_mode(WAL)"} {
		q.Add("_pragma", v)
	}
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, errors.New("cannot open SQLite")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db}
	if err = s.Migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	mode := "production"
	if dev {
		mode = "development"
	}
	if _, err = db.ExecContext(ctx, "INSERT OR IGNORE INTO app_meta(key,value) VALUES('mode',?)", mode); err != nil {
		db.Close()
		return nil, err
	}
	var existing string
	if err = db.QueryRowContext(ctx, "SELECT value FROM app_meta WHERE key='mode'").Scan(&existing); err != nil {
		db.Close()
		return nil, err
	}
	if existing != mode {
		db.Close()
		return nil, errors.New("database mode mismatch: use a separate data directory")
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migration (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)"); err != nil {
		return err
	}
	for _, f := range files {
		var n int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM schema_migration WHERE name=?", f.Name()).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		b, e := migrations.ReadFile("migrations/" + f.Name())
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, string(b)); err != nil {
			return fmt.Errorf("migration %s failed", f.Name())
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migration VALUES(?,?)", f.Name(), timestamp(time.Now())); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Ready(ctx context.Context) error {
	var n int
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM schema_migration").Scan(&n); err != nil {
		return err
	}
	files, _ := migrations.ReadDir("migrations")
	if n != len(files) {
		return errors.New("schema not current")
	}
	return nil
}

func (s *Store) ResetPassword(ctx context.Context, hash string) error {
	if !strings.HasPrefix(hash, "$argon2id$") {
		return errors.New("invalid password hash")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "UPDATE administrator SET password_hash=?,updated_at=? WHERE id=1", hash, timestamp(time.Now()))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("administrator_not_initialized")
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM session"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM passkey_ceremony"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE passkey_state SET epoch=epoch+1 WHERE id=1"); err != nil {
		return err
	}
	return tx.Commit()
}

func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
