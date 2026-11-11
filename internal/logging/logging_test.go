package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestSensitiveFieldsRedacted(t *testing.T) {
	var b bytes.Buffer
	l := New(&b, "info")
	l.Info("example", "token", "synthetic-token", "access_key_id", "synthetic-ak", "password", "synthetic-password", "api_key", "synthetic-api-key")
	for _, v := range []string{"synthetic-token", "synthetic-ak", "synthetic-password", "synthetic-api-key"} {
		if strings.Contains(b.String(), v) {
			t.Fatal("secret leaked")
		}
	}
}
