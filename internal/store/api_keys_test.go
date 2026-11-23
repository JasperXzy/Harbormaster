package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestAPIKeyHashScopePersistenceAndRevocation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	key, token, err := s.CreateAPIKey(ctx, model.APIKeyCreate{Name: "  小组件  ", Scopes: []string{"summary:read"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if key.Name != "小组件" || len(token) != 71 || !strings.HasPrefix(token, "hm_api_") {
		t.Fatal("invalid key creation")
	}
	var stored []byte
	if err := s.DB.QueryRow("SELECT token_hash FROM api_key WHERE id=?", key.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256([]byte(token))
	if !bytes.Equal(stored, wantHash[:]) {
		t.Fatal("token not stored as SHA-256")
	}
	if _, err := s.AuthenticateAPIKey(ctx, token, "instance:control", now); !errors.Is(err, ErrAPIKeyScope) {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "*", "summary", "SUMMARY:READ"} {
		if _, err := s.AuthenticateAPIKey(ctx, token, invalid, now); !errors.Is(err, ErrAPIKeyScope) {
			t.Fatal("unrecognized required scope accepted")
		}
	}
	keys, err := s.APIKeys(ctx, 0, 10)
	if err != nil || len(keys) != 1 || keys[0].LastUsedAt != nil {
		t.Fatal("denial marked key as used", err)
	}
	raw, _ := json.Marshal(keys)
	if bytes.Contains(raw, []byte(token)) || bytes.Contains(raw, []byte("token_hash")) || bytes.Contains(raw, []byte("sequence")) {
		t.Fatal("metadata exposed authentication material")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	usedAt := now.Add(time.Second)
	authenticated, err := s.AuthenticateAPIKey(ctx, token, "summary:read", usedAt)
	if err != nil || authenticated.ID != key.ID || authenticated.LastUsedAt == nil || !authenticated.LastUsedAt.Equal(usedAt) {
		t.Fatal("authentication did not survive restart", err)
	}
	for _, wrong := range []string{"hm_api_" + strings.Repeat("0", 64), "hm_agent_" + strings.Repeat("a", 64), strings.Repeat("b", 64), "hm_api_" + strings.Repeat("z", 64)} {
		if _, err := s.AuthenticateAPIKey(ctx, wrong, "summary:read", now); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("foreign credential accepted", err)
		}
	}
	revokedAt := now.Add(2 * time.Second)
	if err := s.RevokeAPIKey(ctx, key.ID, revokedAt); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAPIKey(ctx, key.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateAPIKey(ctx, token, "summary:read", now.Add(time.Hour)); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("revoked key accepted", err)
	}
	keys, err = s.APIKeys(ctx, 0, 10)
	if err != nil || len(keys) != 1 || keys[0].RevokedAt == nil || !keys[0].RevokedAt.Equal(revokedAt) || !keys[0].LastUsedAt.Equal(usedAt) {
		t.Fatal("revocation lost history", err)
	}
	if err := s.RevokeAPIKey(ctx, "missing", now); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestAPIKeyValidationAndStablePagination(t *testing.T) {
	s := notificationStore(t)
	ctx := context.Background()
	now := time.Now()
	for _, in := range []model.APIKeyCreate{
		{Name: " ", Scopes: []string{"summary:read"}},
		{Name: "name", Scopes: []string{}},
		{Name: "name", Scopes: []string{"*"}},
		{Name: "name", Scopes: []string{"summary:read", "summary:read"}},
		{Name: "line\nbreak", Scopes: []string{"cron:run"}},
	} {
		if _, token, err := s.CreateAPIKey(ctx, in, now); !errors.Is(err, ErrAPIKeyConfig) || token != "" {
			t.Fatal("invalid key created")
		}
	}
	var ids []string
	for range 3 {
		key, _, err := s.CreateAPIKey(ctx, model.APIKeyCreate{Name: "key", Scopes: []string{"summary:read", "cron:run", "instance:control"}}, now)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, key.ID)
	}
	first, err := s.APIKeys(ctx, 0, 2)
	if err != nil || len(first) != 2 || first[0].ID != ids[2] || first[1].ID != ids[1] {
		t.Fatal(first, err)
	}
	if _, _, err := s.CreateAPIKey(ctx, model.APIKeyCreate{Name: "new", Scopes: []string{"summary:read"}}, now); err != nil {
		t.Fatal(err)
	}
	next, err := s.APIKeys(ctx, first[1].Sequence, 2)
	if err != nil || len(next) != 1 || next[0].ID != ids[0] {
		t.Fatal("concurrent insertion broke pagination", next, err)
	}
}
