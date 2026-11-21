package model

import "time"

type UpgradeCommands struct {
	DockerCompose string `json:"docker_compose"`
	BinaryHint    string `json:"binary_hint"`
	AgentHint     string `json:"agent_hint"`
}

type VersionCenter struct {
	ServerVersion       string          `json:"server_version"`
	ServerCommit        string          `json:"server_commit"`
	ServerBuildTime     string          `json:"server_build_time"`
	BundledAgentVersion *string         `json:"bundled_agent_version"`
	DeploymentMode      string          `json:"deployment_mode"`
	CheckedAt           *time.Time      `json:"checked_at"`
	LatestVersion       *string         `json:"latest_version"`
	ReleaseURL          *string         `json:"release_url"`
	ReleaseNotes        *string         `json:"release_notes"`
	UpdateAvailable     *bool           `json:"update_available"`
	LastError           *APIError       `json:"last_error"`
	UpgradeCommands     UpgradeCommands `json:"upgrade_commands"`
}

type PublicBranding struct {
	Name      string     `json:"name"`
	HasLogo   bool       `json:"has_logo"`
	UpdatedAt *time.Time `json:"updated_at"`
}
