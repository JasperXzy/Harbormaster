package m0

import (
	"encoding/base64"
	"net/url"
	"strings"

	"github.com/JasperXzy/harbormaster/internal/security"
)

func scrubText(s string, p profile) string {
	for _, secret := range []string{p.AccessKeySecret, p.SecurityToken, p.AccessKeyID} {
		if secret == "" {
			continue
		}
		for _, form := range []string{secret, url.QueryEscape(secret), base64.StdEncoding.EncodeToString([]byte(secret))} {
			s = strings.ReplaceAll(s, form, "[redacted]")
		}
	}
	return s
}
func scrub(v any, p profile) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, value := range x {
			key := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(k))
			safeKey := scrubText(k, p)
			if key == "accesskeyid" {
				if id, ok := value.(string); ok {
					out[safeKey] = security.MaskAccessKeyID(id)
				} else {
					out[safeKey] = "[redacted]"
				}
				continue
			}
			hide := false
			for _, term := range []string{"secret", "token", "password", "authorization", "cookie", "signature", "privatekey", "userdata", "message", "recommend"} {
				if strings.Contains(key, term) {
					hide = true
				}
			}
			if hide {
				out[safeKey] = "[redacted]"
			} else {
				out[safeKey] = scrub(value, p)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, value := range x {
			out[i] = scrub(value, p)
		}
		return out
	case string:
		x = scrubText(x, p)
		if u, err := url.Parse(x); err == nil && u.Host != "" {
			u.User = nil
			u.RawQuery = ""
			u.Fragment = ""
			return u.String()
		}
		return x
	default:
		return v
	}
}
