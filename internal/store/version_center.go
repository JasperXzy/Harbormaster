package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentrelease"
	"github.com/JasperXzy/harbormaster/internal/buildinfo"
	"github.com/JasperXzy/harbormaster/internal/model"
)

func (s *Store) VersionCenter(ctx context.Context, bundledAgent *string, publicURL string) (model.VersionCenter, error) {
	out := model.VersionCenter{
		ServerVersion:       buildinfo.Version,
		ServerCommit:        buildinfo.Commit,
		ServerBuildTime:     buildinfo.BuildTime,
		BundledAgentVersion: bundledAgent,
		DeploymentMode:      "readonly_container",
		UpgradeCommands: model.UpgradeCommands{
			DockerCompose: "docker pull ghcr.io/jasperxzy/harbormaster:latest && docker compose --env-file runtime.env -f compose.yml -f compose.readonly.yml up -d hm-server",
			BinaryHint:    "下载对应平台新版本 hm-server 二进制替换后执行 systemctl restart harbormaster-server",
			AgentHint:     "在实例页点击带有「有新版本」提示的机器，复制新安装命令在实例上重新执行即可平滑升级 hm-agent",
		},
	}
	if strings.TrimSpace(publicURL) != "" {
		out.UpgradeCommands.AgentHint = "在实例页点击带有「有新版本」提示的机器，生成来自 " + strings.TrimRight(publicURL, "/") + " 的一次性升级命令并在实例上执行即可平滑升级 hm-agent"
	}
	var checked string
	var latest, url, notes, code sql.NullString
	err := s.DB.QueryRowContext(ctx, "SELECT checked_at,latest_version,release_url,release_notes,error_code FROM version_check_cache WHERE id=1").Scan(&checked, &latest, &url, &notes, &code)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if t, e := time.Parse(time.RFC3339Nano, checked); e == nil {
		out.CheckedAt = &t
	}
	if latest.Valid && latest.String != "" {
		v := latest.String
		out.LatestVersion = &v
		out.UpdateAvailable = agentrelease.UpdateAvailable(buildinfo.Version, v)
	}
	if url.Valid && url.String != "" {
		u := url.String
		out.ReleaseURL = &u
	}
	if notes.Valid && notes.String != "" {
		n := notes.String
		out.ReleaseNotes = &n
	}
	if code.Valid && code.String != "" {
		out.LastError = model.Error(code.String)
	}
	return out, nil
}

func (s *Store) SaveVersionCheck(ctx context.Context, latest, releaseURL, notes, errCode string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var lVal, uVal, nVal, eVal any
	if latest != "" {
		lVal = latest
	}
	if releaseURL != "" {
		uVal = releaseURL
	}
	if notes != "" {
		if len(notes) > 4096 {
			notes = notes[:4096]
		}
		nVal = notes
	}
	if errCode != "" {
		eVal = errCode
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO version_check_cache(id,checked_at,latest_version,release_url,release_notes,error_code)
 VALUES(1,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET checked_at=excluded.checked_at,
 latest_version=CASE WHEN excluded.error_code IS NULL THEN excluded.latest_version ELSE version_check_cache.latest_version END,
 release_url=CASE WHEN excluded.error_code IS NULL THEN excluded.release_url ELSE version_check_cache.release_url END,
 release_notes=CASE WHEN excluded.error_code IS NULL THEN excluded.release_notes ELSE version_check_cache.release_notes END,
 error_code=excluded.error_code`, timestamp(now), lVal, uVal, nVal, eVal); err != nil {
		return err
	}
	details := model.AuditDetails{ChangedFields: []string{"checked_at"}}
	if latest != "" {
		details.After = map[string]any{"latest_version": latest}
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "version_checked", TargetType: "system", TargetID: "version_center", Details: details}, now); err != nil {
		return err
	}
	return tx.Commit()
}
