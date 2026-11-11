package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/security"
)

// SeedDemo contains fabricated IDs and no cloud credentials. All values are
// isolated in a development-only database. Repeated starts preserve samples.
func (s *Store) SeedDemo(ctx context.Context) error {
	var mode string
	if err := s.DB.QueryRowContext(ctx, "SELECT value FROM app_meta WHERE key='mode'").Scan(&mode); err != nil {
		return err
	}
	if mode != "development" {
		return errors.New("demo data requires development database")
	}
	hash, err := security.HashPassword([]byte("demo-password-only"))
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := timestamp(time.Now())
	for _, a := range [][3]string{{"demo-account-1", "Example account A", "china"}, {"demo-account-2", "Example account B", "international"}} {
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO account VALUES(?,?,?,?)", a[0], a[1], a[2], now); err != nil {
			return err
		}
	}
	for _, i := range [][6]string{{"demo-sin", "demo-account-1", "i-demo-sin", "ecs", "sin-pro (demo)", "ap-southeast-1"}, {"demo-hkg", "demo-account-1", "i-demo-hkg", "ecs", "hkg-pro (demo)", "cn-hongkong"}, {"demo-sha", "demo-account-2", "swas-demo-sha", "swas", "sha-pro (demo)", "cn-shanghai"}} {
		if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO instance VALUES(?,?,?,?,?,?, 'running','running',?)", i[0], i[1], i[2], i[3], i[4], i[5], now); err != nil {
			return err
		}
	}
	h := sha256.Sum256([]byte(agentproto.DemoToken))
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO agent(id,instance_id,token_hash) VALUES(?,?,?)", agentproto.DemoAgentID, "demo-sin", h[:]); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO administrator VALUES(1,?,?)", hash, now); err != nil {
		return err
	}
	return tx.Commit()
}
