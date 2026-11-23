package aliyun

import (
	"errors"
	"net/http"
)

// writeHTTP permits only one SDK attempt, including when the first response is
// lost. The fixed destination and response bounds are enforced by its gate.
type writeHTTP struct {
	gate      readHTTP
	attempted bool
	status    int
}

func (g *writeHTTP) Call(r *http.Request, transport *http.Transport) (*http.Response, error) {
	if g.attempted {
		return nil, errors.New("write_retry_not_allowed")
	}
	if err := r.Context().Err(); err != nil {
		return nil, errors.New("write_request_cancelled")
	}
	g.attempted = true
	response, err := g.gate.Call(r, transport)
	if response != nil {
		g.status = response.StatusCode
	}
	return response, err
}
