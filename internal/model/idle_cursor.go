package model

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
)

type idleCursor struct{ Account, Region, Kind, After string }

func EncodeIdleCursor(after string, f IdleFilter) string {
	raw, _ := json.Marshal(idleCursor{f.AccountID, f.RegionID, f.Kind, after})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func DecodeIdleCursor(raw string, f IdleFilter) (string, bool) {
	if len(raw) > 4096 {
		return "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", false
	}
	var c idleCursor
	d := json.NewDecoder(bytes.NewReader(decoded))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Account != f.AccountID || c.Region != f.RegionID || c.Kind != f.Kind || c.After == "" || len(c.After) > 1024 {
		return "", false
	}
	return c.After, true
}
