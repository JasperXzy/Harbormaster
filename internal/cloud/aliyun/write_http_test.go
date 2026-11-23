package aliyun

import (
	"context"
	"net/http"
	"testing"
)

func TestWriteGateDoesNotSendCancelledRequest(t *testing.T) {
	client := NewClient(roundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("cancelled write reached transport")
		return nil, nil
	}), 0)
	gate := writeHTTP{gate: readHTTP{client: client.http, endpoint: "ecs.cn-shanghai.aliyuncs.com", action: "DeleteInstance", version: "2014-05-26", method: "POST"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://ecs.cn-shanghai.aliyuncs.com/", nil)
	req.Header.Set("x-acs-action", "DeleteInstance")
	req.Header.Set("x-acs-version", "2014-05-26")
	if _, err := gate.Call(req, nil); err == nil || err.Error() != "write_request_cancelled" || gate.attempted {
		t.Fatal(err, gate.attempted)
	}
}
