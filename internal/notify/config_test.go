package notify

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
	"github.com/JasperXzy/harbormaster/internal/telegram"
)

func TestChannelIsEncryptedAndUsesRevisionCompareAndSwap(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := store.Open(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	box, _ := security.NewBox(bytes.Repeat([]byte{3}, 32))
	channels := Channels{Store: s, Box: box}
	if _, _, err = channels.Load(ctx); !errors.Is(err, store.ErrTelegramNotConfigured) {
		t.Fatal(err)
	}
	d := telegram.Destination{Token: "123456789:TEST_ONLY_not_a_real_bot_token", ChatID: "-100123456789", ProxyMode: "socks5", ProxyURL: "socks5://127.0.0.1:1080", ProxyUsername: "test-private-user", ProxyPassword: "test-private-password"}
	state, err := channels.Replace(ctx, d, 0, time.Now())
	if err != nil || state.Revision != 1 {
		t.Fatal(state, err)
	}
	loaded, saved, err := channels.Load(ctx)
	if err != nil || loaded != d || saved.Revision != 1 {
		t.Fatal("encrypted round trip failed", err)
	}
	for _, file := range []string{"harbormaster.db", "harbormaster.db-wal"} {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{d.Token, d.ProxyUsername, d.ProxyPassword} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatal("plaintext channel credential found in SQLite or WAL")
			}
		}
	}
	state, err = channels.Replace(ctx, d, 1, time.Now())
	if err != nil || state.Revision != 1 {
		t.Fatal("unchanged save invalidated queued work")
	}
	changed := d
	changed.ChatID = "-100987654321"
	if _, err = channels.Replace(ctx, changed, 0, time.Now()); !errors.Is(err, store.ErrChannelChanged) {
		t.Fatal("stale writer replaced channel", err)
	}
	if state, err = channels.Replace(ctx, changed, 1, time.Now()); err != nil || state.Revision != 2 {
		t.Fatal(state, err)
	}
	wrongBox, _ := security.NewBox(bytes.Repeat([]byte{4}, 32))
	wrong := Channels{Store: s, Box: wrongBox}
	if _, _, err = wrong.Load(ctx); !errors.Is(err, ErrConfiguration) {
		t.Fatal("wrong key accepted", err)
	}
	if _, err = wrong.Replace(ctx, d, 2, time.Now()); !errors.Is(err, ErrConfiguration) {
		t.Fatal("wrong key overwrote existing credentials", err)
	}
}
