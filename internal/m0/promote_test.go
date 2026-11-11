package m0

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func TestPromotionKeepsSourceAndDestinationKeysSeparate(t *testing.T) {
	path, key := pathsForTest(t)
	p := exampleProfile()
	p.SecurityToken = ""
	if err := saveProfile(path, key, p, false); err != nil {
		t.Fatal(err)
	}
	// Destination environment key is intentionally different from the source.
	t.Setenv("HM_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	calls := 0
	err := ImportAccounts(path, key, func(in model.AccountCreate) error {
		calls++
		if in.Name != p.Name || in.AccessKeyID != p.AccessKeyID || in.AccessKeySecret != p.AccessKeySecret {
			t.Fatal("source credential changed")
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatal("source key overridden by destination", calls, err)
	}
}
