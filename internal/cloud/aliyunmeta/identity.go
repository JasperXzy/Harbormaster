// Package aliyunmeta reads the two identity fields required by Harbormaster.
// It does not request RAM credentials, user data or arbitrary metadata paths.
package aliyunmeta

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("aliyun_metadata_unavailable")

type Identity struct {
	InstanceID string
	RegionID   string
}

var instancePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var regionPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)+$`)

func (i Identity) Valid() bool {
	return instancePattern.MatchString(i.InstanceID) && len(i.RegionID) <= 64 && regionPattern.MatchString(i.RegionID)
}

// Read always uses the fixed IMDSv2 endpoint and a five-second total budget.
// The optional client permits test transports; standard transports are cloned
// with proxies disabled. Caller settings are never mutated, cookies never sent,
// and redirects never followed, even if the supplied client allows them.
func Read(ctx context.Context, client *http.Client) (Identity, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var requestClient http.Client
	if client != nil {
		requestClient = *client
	}
	requestClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	requestClient.Jar = nil
	if requestClient.Timeout <= 0 || requestClient.Timeout > 5*time.Second {
		requestClient.Timeout = 5 * time.Second
	}
	var transport *http.Transport
	if requestClient.Transport == nil {
		dialer := &net.Dialer{Timeout: time.Second}
		transport = &http.Transport{DialContext: dialer.DialContext, ResponseHeaderTimeout: 2 * time.Second}
	}
	if existing, ok := requestClient.Transport.(*http.Transport); ok {
		transport = existing.Clone()
	}
	if transport != nil {
		transport.Proxy = nil
		transport.DisableKeepAlives = true
		requestClient.Transport = transport
		defer transport.CloseIdleConnections()
	}
	const base = "http://100.100.100.200/latest/"
	read := func(method, path, token string) (string, error) {
		req, err := http.NewRequestWithContext(ctx, method, base+path, nil)
		if err != nil {
			return "", ErrUnavailable
		}
		if method == "PUT" {
			req.Header.Set("X-aliyun-ecs-metadata-token-ttl-seconds", "60")
		} else {
			req.Header.Set("X-aliyun-ecs-metadata-token", token)
		}
		res, err := requestClient.Do(req)
		if err != nil {
			return "", ErrUnavailable
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return "", ErrUnavailable
		}
		raw, err := io.ReadAll(io.LimitReader(res.Body, 4097))
		if err != nil || len(raw) > 4096 {
			return "", ErrUnavailable
		}
		return strings.TrimSpace(string(raw)), nil
	}
	token, err := read("PUT", "api/token", "")
	if err != nil || token == "" || strings.ContainsAny(token, "\r\n") {
		return Identity{}, ErrUnavailable
	}
	id, err := read("GET", "meta-data/instance-id", token)
	if err != nil {
		return Identity{}, ErrUnavailable
	}
	region, err := read("GET", "meta-data/region-id", token)
	if err != nil {
		return Identity{}, ErrUnavailable
	}
	result := Identity{InstanceID: id, RegionID: region}
	if !result.Valid() {
		return Identity{}, ErrUnavailable
	}
	return result, nil
}
