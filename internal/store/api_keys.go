package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var ErrAPIKeyScope = errors.New("insufficient_scope")
var ErrAPIKeyConfig = errors.New("invalid_api_key_configuration")

const keyColumns = "sequence,id,name,scopes_json,created_at,last_used_at,revoked_at"

func readAPIKey(row rowScanner) (model.APIKey, error) {
	var key model.APIKey
	var scopes, created string
	var used, revoked sql.NullString
	err := row.Scan(&key.Sequence, &key.ID, &key.Name, &scopes, &created, &used, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return key, ErrNotFound
	}
	if err != nil {
		return key, err
	}
	if err = json.Unmarshal([]byte(scopes), &key.Scopes); err != nil {
		return key, err
	}
	if len((model.APIKeyCreate{Name: key.Name, Scopes: key.Scopes}).Validate()) != 0 {
		return key, ErrAPIKeyConfig
	}
	key.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return key, err
	}
	key.LastUsedAt, key.RevokedAt = parseAgentTime(used), parseAgentTime(revoked)
	// Revocation never becomes valid again because of a malformed timestamp.
	if revoked.Valid && key.RevokedAt == nil {
		return key, ErrAPIKeyConfig
	}
	return key, nil
}

func (s *Store) CreateAPIKey(ctx context.Context, in model.APIKeyCreate, now time.Time) (model.APIKey, string, error) {
	if len(in.Validate()) != 0 {
		return model.APIKey{}, "", ErrAPIKeyConfig
	}
	key := model.APIKey{ID: model.NewID("key_"), Name: strings.TrimSpace(in.Name), Scopes: append([]string{}, in.Scopes...), CreatedAt: now.UTC()}
	sort.Strings(key.Scopes)
	var random [32]byte
	_, _ = rand.Read(random[:])
	token := "hm_api_" + hex.EncodeToString(random[:])
	hash := sha256.Sum256([]byte(token))
	scopes, err := json.Marshal(key.Scopes)
	if err != nil {
		return model.APIKey{}, "", err
	}
	result, err := s.DB.ExecContext(ctx, "INSERT INTO api_key(id,name,scopes_json,token_hash,created_at) VALUES(?,?,?,?,?)", key.ID, key.Name, string(scopes), hash[:], timestamp(now))
	if err != nil {
		return model.APIKey{}, "", err
	}
	key.Sequence, err = result.LastInsertId()
	if err != nil {
		return model.APIKey{}, "", err
	}
	return key, token, nil
}

func (s *Store) APIKeys(ctx context.Context, before int64, limit int) ([]model.APIKey, error) {
	if before < 0 || limit < 1 || limit > 201 {
		return nil, ErrAPIKeyConfig
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT "+keyColumns+" FROM api_key WHERE (?=0 OR sequence<?) ORDER BY sequence DESC LIMIT ?", before, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []model.APIKey{}
	for rows.Next() {
		key, err := readAPIKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) RevokeAPIKey(ctx context.Context, id string, now time.Time) error {
	result, err := s.DB.ExecContext(ctx, "UPDATE api_key SET revoked_at=coalesce(revoked_at,?) WHERE id=?", timestamp(now), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Consumers must name exactly one permitted scope. This authentication is
// separate from browser sessions and agent-report tokens, with no wildcard.
func (s *Store) AuthenticateAPIKey(ctx context.Context, token, requiredScope string, now time.Time) (model.APIKey, error) {
	if !model.ValidAPIKeyScope(requiredScope) {
		return model.APIKey{}, ErrAPIKeyScope
	}
	if len(token) != 71 || !strings.HasPrefix(token, "hm_api_") {
		return model.APIKey{}, ErrUnauthorized
	}
	if _, err := hex.DecodeString(token[7:]); err != nil {
		return model.APIKey{}, ErrUnauthorized
	}
	hash := sha256.Sum256([]byte(token))
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.APIKey{}, err
	}
	defer tx.Rollback()
	key, err := readAPIKey(tx.QueryRowContext(ctx, "SELECT "+keyColumns+" FROM api_key WHERE token_hash=? AND revoked_at IS NULL", hash[:]))
	if errors.Is(err, ErrNotFound) {
		return model.APIKey{}, ErrUnauthorized
	}
	if err != nil {
		return model.APIKey{}, err
	}
	allowed := false
	for _, scope := range key.Scopes {
		if scope == requiredScope {
			allowed = true
		}
	}
	if !allowed {
		return model.APIKey{}, ErrAPIKeyScope
	}
	_, err = tx.ExecContext(ctx, "UPDATE api_key SET last_used_at=? WHERE id=?", timestamp(now), key.ID)
	if err != nil {
		return model.APIKey{}, err
	}
	at := now.UTC()
	key.LastUsedAt = &at
	return key, tx.Commit()
}
