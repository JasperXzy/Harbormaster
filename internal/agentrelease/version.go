package agentrelease

import (
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

// NormalizeVersion accepts a full semantic version, with optional v prefix,
// or the explicit development label. Input is also safe for a linker -X value.
func NormalizeVersion(value string) (string, bool) {
	if value == "dev" {
		return value, true
	}
	if len(value) == 0 || len(value) > 64 {
		return "", false
	}
	if !strings.HasPrefix(value, "v") {
		value = "v" + value
	}
	if len(value) > 64 || !semver.IsValid(value) {
		return "", false
	}
	core := strings.SplitN(strings.SplitN(value, "+", 2)[0], "-", 2)[0]
	if strings.Count(core, ".") != 2 {
		return "", false
	}
	return value, true
}

func UpdateAvailable(installed, available string) *bool {
	installed, validInstalled := NormalizeVersion(installed)
	available, validAvailable := NormalizeVersion(available)
	if !validInstalled || !validAvailable || installed == "dev" || available == "dev" {
		return nil
	}
	newer := semver.Compare(installed, available) < 0
	return &newer
}

// VersionCache avoids hashing both executable files on every SSE tick/request.
// Install commands always call Check directly and never rely on this hint.
type VersionCache struct {
	mu      sync.Mutex
	dir     string
	checked time.Time
	version *string
}

func (c *VersionCache) Get(dir string, now time.Time) *string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dir == dir && !c.checked.IsZero() && !now.Before(c.checked) && now.Before(c.checked.Add(30*time.Second)) {
		return c.version
	}
	c.dir, c.checked, c.version = dir, now, nil
	if manifest, err := Check(dir); err == nil {
		v := manifest.Version
		c.version = &v
	}
	return c.version
}
