package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentrelease"
)

const defaultReleaseFeedURL = "https://api.github.com/repos/JasperXzy/harbormaster/releases/latest"

func (p *productionPanel) versionCenter(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/system/version-center" && path != "/system/version-center/check" {
		return false
	}
	now := p.now().UTC()
	bundled := p.releaseVersions.Get(p.agentReleaseDir, now)
	if path == "/system/version-center" {
		if r.Method != http.MethodGet {
			writeError(w, 405, "method_not_allowed")
			return true
		}
		vc, err := p.s.VersionCenter(r.Context(), bundled, p.publicURL)
		if err != nil {
			businessError(w, err)
		} else {
			writeJSON(w, 200, vc)
		}
		return true
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	feedURL := p.releaseFeedURL
	if feedURL == "" {
		feedURL = defaultReleaseFeedURL
	}
	latest, relURL, notes, errCode := fetchLatestRelease(r.Context(), feedURL)
	if err := p.s.SaveVersionCheck(r.Context(), latest, relURL, notes, errCode, now); err != nil {
		businessError(w, err)
		return true
	}
	vc, err := p.s.VersionCenter(r.Context(), bundled, p.publicURL)
	if err != nil {
		businessError(w, err)
		return true
	}
	writeJSON(w, 200, vc)
	return true
}

func fetchLatestRelease(parent context.Context, feedURL string) (latest, relURL, notes, errCode string) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return "", "", "", "release_feed_unreachable"
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "harbormaster-version-center")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", "release_feed_unreachable"
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", "", "", "no_published_release"
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", "", "release_feed_unavailable"
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", "", "release_feed_unavailable"
	}
	var payload struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return "", "", "", "release_feed_invalid"
	}
	norm, ok := agentrelease.NormalizeVersion(strings.TrimSpace(payload.TagName))
	if !ok || norm == "dev" {
		return "", "", "", "release_feed_invalid"
	}
	u := strings.TrimSpace(payload.HTMLURL)
	if u != "" && !strings.HasPrefix(u, "https://") {
		u = ""
	}
	return strings.TrimPrefix(norm, "v"), u, strings.TrimSpace(payload.Body), ""
}
