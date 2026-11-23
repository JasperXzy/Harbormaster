package model

import "context"

type apiKeyPrincipal struct{}

// WithAPIKeyPrincipal is set only after HTTP bearer authentication. The store
// rechecks this key and its scope atomically with each action enqueue.
func WithAPIKeyPrincipal(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, apiKeyPrincipal{}, id)
}
func APIKeyPrincipal(ctx context.Context) string {
	id, _ := ctx.Value(apiKeyPrincipal{}).(string)
	return id
}
