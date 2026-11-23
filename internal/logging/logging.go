package logging

import (
	"io"
	"log/slog"
	"strings"
)

// New emits structured logs. Callers only log event codes and explicit safe
// fields: never raw SDK/HTTP errors, URLs, headers, request bodies or Config.
func New(w io.Writer, level string) *slog.Logger {
	l := slog.LevelInfo
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: l, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		k := strings.ToLower(a.Key)
		for _, s := range []string{"secret", "token", "password", "authorization", "cookie", "master_key", "access_key", "accesskey", "api_key"} {
			if strings.Contains(k, s) {
				return slog.String(a.Key, "[redacted]")
			}
		}
		if a.Key == slog.TimeKey {
			return slog.String(a.Key, a.Value.Time().UTC().Format("2006-01-02T15:04:05.000Z"))
		}
		return a
	}}))
}
