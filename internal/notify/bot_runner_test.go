package notify

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
	"github.com/JasperXzy/harbormaster/internal/telegram"
)

type fakeBot struct {
	updates   []telegram.Update
	replies   []string
	buttons   [][][]telegram.InlineButton
	callbacks []string
}

func (f *fakeBot) GetUpdates(_ context.Context, _ telegram.Destination, offset int64, _ int) ([]telegram.Update, error) {
	out := []telegram.Update{}
	for _, u := range f.updates {
		if u.UpdateID >= offset {
			out = append(out, u)
		}
	}
	return out, nil
}

func (f *fakeBot) ReplyToChat(_ context.Context, _ telegram.Destination, _ string, text string, btns [][]telegram.InlineButton) (telegram.Receipt, error) {
	f.replies = append(f.replies, text)
	f.buttons = append(f.buttons, btns)
	return telegram.Receipt{MessageID: int64(len(f.replies))}, nil
}

func (f *fakeBot) AnswerCallback(_ context.Context, _ telegram.Destination, _ string, text string) error {
	f.callbacks = append(f.callbacks, text)
	return nil
}

func TestTelegramRemoteControlWhitelistTwoStepConfirmAndIntentButtons(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	box, _ := security.NewBox(bytes.Repeat([]byte{3}, 32))
	if err := s.BindMasterKey(ctx, box); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	a := model.Account{ID: "acc1", Name: "main-acc", Site: "china", Enabled: true, RegionIDs: []string{"cn-shanghai"}, QuotaMainland: 20 << 30, QuotaOverseas: 200 << 30, ResetDay: 1, UpdatedAt: now}
	if _, err := s.CreateAccount(ctx, a, bytes.Repeat([]byte{9}, 32), time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"INSERT INTO instance VALUES('inst1','acc1','i-cloud1','ecs','hkg-pro','cn-shanghai','running','running','2026-11-18T12:00:00Z')",
		`INSERT INTO resource_snapshot VALUES('acc1','ecs','inst1','cn-shanghai','{"id":"inst1","account_id":"acc1","name":"hkg-pro","kind":"ecs","status":"running","charge_type":"pay_as_you_go","cloud_instance_id":"i-cloud1"}',1,'2026-11-18T12:00:00Z')`,
	} {
		if _, err := s.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	ch := Channels{Store: s, Box: box}
	if _, err := ch.Update(ctx, Update{BotToken: new("123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ123456"), ChatID: "-10012345", ProxyMode: "none"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveTelegramControl(ctx, model.TelegramControlSettings{Revision: 0, Enabled: true, AllowedUserIDs: []string{"888888"}}, now); err != nil {
		t.Fatal(err)
	}

	bot := &fakeBot{
		updates: []telegram.Update{
			// Unauthorized user 999999 -> silently ignored!
			{UpdateID: 1, Message: &telegram.BotMessage{MessageID: 1, From: telegram.BotUser{ID: 999999}, Chat: telegram.BotChat{ID: 999999}, Text: "/status"}},
			// Authorized user 888888 -> /status and /stop hkg-pro
			{UpdateID: 2, Message: &telegram.BotMessage{MessageID: 2, From: telegram.BotUser{ID: 888888}, Chat: telegram.BotChat{ID: -10012345}, Text: "/status"}},
			{UpdateID: 3, Message: &telegram.BotMessage{MessageID: 3, From: telegram.BotUser{ID: 888888}, Chat: telegram.BotChat{ID: -10012345}, Text: "/stop hkg-pro"}},
		},
	}
	runner := BotRunner{Channels: ch, Bot: bot, PowerEnabled: true, Timezone: "Asia/Shanghai", Now: func() time.Time { return now }}
	runner.Step(ctx, 0)

	if len(bot.replies) != 2 || !strings.Contains(bot.replies[0], "状态摘要") || !strings.Contains(bot.replies[1], "确认要将 hkg-pro 关机吗") {
		t.Fatal("unexpected bot replies", bot.replies)
	}
	if len(bot.buttons[1]) == 0 || len(bot.buttons[1][0]) < 2 {
		t.Fatal("expected ordinary + stop-charging buttons", bot.buttons[1])
	}
	cbData := bot.buttons[1][0][0].CallbackData // act:tga_...

	// Click confirmation button as authorized user -> enqueues power job with trigger=telegram!
	bot.updates = append(bot.updates, telegram.Update{
		UpdateID:      4,
		CallbackQuery: &telegram.BotCallbackQuery{ID: "cb1", From: telegram.BotUser{ID: 888888}, Message: &telegram.BotMessage{Chat: telegram.BotChat{ID: -10012345}}, Data: cbData},
	})
	runner.Step(ctx, 0)

	op, err := s.ClaimPowerJob(ctx, now.Add(time.Second))
	if err != nil || op.InstanceID != "inst1" || op.Action != "stop" || op.Job.Trigger != "telegram" {
		t.Fatal("expected telegram-triggered stop power job", err, op)
	}
}
