#!/bin/sh
# Harbormaster agent installer v1. Registration code is stdin, never argv/env.
set -eu
umask 077
fail() { printf '%s\n' "$1" >&2; exit 1; }
[ "$(id -u)" = 0 ] || fail 'installer_requires_root'
[ "$(uname -s)" = Linux ] || fail 'installer_requires_linux'
[ "$#" = 2 ] || fail 'installer_requires_origin_and_release_digest'
origin=$1
release_digest=$2
case "$origin" in https://*) ;; *) fail 'https_origin_required';; esac
case "$release_digest" in *[!a-f0-9]*|'') fail 'invalid_release_digest';; esac
[ "${#release_digest}" = 64 ] || fail 'invalid_release_digest'
for tool in curl sha256sum openssl systemctl useradd getent install ip awk sort mktemp; do
  command -v "$tool" >/dev/null 2>&1 || fail "missing_dependency_$tool"
done
systemd_version=$(systemctl --version | awk 'NR==1 {print $2}')
case "$systemd_version" in *[!0-9]*|'') fail 'cannot_read_systemd_version';; esac
[ "$systemd_version" -ge 247 ] || fail 'systemd_247_or_later_required'
case "$(uname -m)" in x86_64) arch=amd64;; aarch64|arm64) arch=arm64;; *) fail 'unsupported_architecture';; esac
IFS= read -r registration_code || fail 'registration_code_required_on_stdin'
case "$registration_code" in hm_reg_*) ;; *) fail 'invalid_registration_code';; esac
case "$registration_code" in *[!a-z0-9_]*) fail 'invalid_registration_code';; esac
[ "${#registration_code}" = 71 ] || fail 'invalid_registration_code'

cfg=/etc/harbormaster
state=/var/lib/harbormaster-agent
binary=/usr/local/lib/harbormaster/hm-agent
unit=/etc/systemd/system/hm-agent.service
managed=$cfg/agent-managed
# Unknown existing files/users are not adopted or overwritten.
if [ ! -f "$managed" ]; then
  for path in "$binary" "$unit" "$cfg/agent.env" "$cfg/agent.key" "$state"; do
    [ ! -e "$path" ] && [ ! -L "$path" ] || fail 'unmanaged_agent_files_present'
  done
  if getent passwd hm-agent >/dev/null; then fail 'unmanaged_agent_user_present'; fi
fi
# Only an unambiguous public boundary is selected automatically. Operators can
# retain an explicitly reviewed interface list in the local non-secret env file.
report_interval=10s
if [ -f "$cfg/agent.env" ]; then
  saved_interval=$(awk -F= '$1=="HM_AGENT_REPORT_INTERVAL" {print $2}' "$cfg/agent.env")
  if [ -n "$saved_interval" ]; then report_interval=$saved_interval; fi
  interfaces=$(awk -F= '$1=="HM_AGENT_INTERFACES" {print $2}' "$cfg/agent.env")
else
  interfaces=$({ ip -o -4 route show default; ip -o -6 route show default; } | awk '{for(i=1;i<=NF;i++) if($i=="dev") print $(i+1)}' | sort -u)
fi
case "$interfaces" in ''|*[!a-zA-Z0-9_.:,-]*) fail 'public_interfaces_require_local_configuration';; esac
[ "${#interfaces}" -le 127 ] || fail 'invalid_public_interfaces'
printf '%s\n' "$interfaces" | awk -F, 'NF<1 || NF>8 {exit 1} {for(i=1;i<=NF;i++) if(length($i)<1 || length($i)>15 || $i=="lo") exit 1}' || fail 'invalid_public_interfaces'

# Canonical whole-second values only; preserve the reviewed local cadence on upgrade.
case "$report_interval" in [1-9]s|[1-5][0-9]s|60s) ;; *) fail 'invalid_report_interval';; esac

stage=$(mktemp -d /var/tmp/hm-agent-install.XXXXXX)
mutated=0
success=0
was_active=0
was_enabled=0
had_binary=0
had_unit=0
had_env=0
if [ -f "$binary" ]; then cp -p "$binary" "$stage/old-binary"; had_binary=1; fi
if [ -f "$unit" ]; then cp -p "$unit" "$stage/old-unit"; had_unit=1; fi
if [ -f "$cfg/agent.env" ]; then cp -p "$cfg/agent.env" "$stage/old-env"; had_env=1; fi
systemctl is-active --quiet hm-agent.service && was_active=1 || :
systemctl is-enabled --quiet hm-agent.service && was_enabled=1 || :
finish() {
  result=$?
  set +e
  trap - EXIT HUP INT TERM
  if [ "$mutated" = 1 ] && [ "$success" = 0 ]; then
    systemctl stop hm-agent.service >/dev/null 2>&1 || :
    # Do not remove a pre-existing installation's counters on failed upgrade.
    if [ "$had_binary" = 0 ] && [ -x "$stage/hm-agent-linux-$arch" ]; then
      HM_MASTER_KEY_FILE="$cfg/agent.key" "$stage/hm-agent-linux-$arch" remove-counters --interfaces "$interfaces" >/dev/null 2>&1 || :
    fi
    if [ "$had_binary" = 1 ]; then cp -p "$stage/old-binary" "$binary"; else rm -f "$binary"; fi
    if [ "$had_unit" = 1 ]; then cp -p "$stage/old-unit" "$unit"; else rm -f "$unit"; fi
    if [ "$had_env" = 1 ]; then cp -p "$stage/old-env" "$cfg/agent.env"; else rm -f "$cfg/agent.env"; fi
    systemctl daemon-reload >/dev/null 2>&1 || :
    if [ "$was_enabled" = 1 ]; then systemctl enable hm-agent.service >/dev/null 2>&1 || :; else systemctl disable hm-agent.service >/dev/null 2>&1 || :; fi
    if [ -d "$state" ]; then chown -R hm-agent:hm-agent "$state"; fi
    if [ "$was_active" = 1 ]; then systemctl start hm-agent.service >/dev/null 2>&1 || :; fi
    printf '%s\n' 'installation_failed_restore_attempted_state_and_key_retained' >&2
  fi
  # Credentials may already have rotated remotely; never restore/delete state.
  if [ "$mutated" = 1 ] && [ -d "$state" ]; then chown -R hm-agent:hm-agent "$state"; fi
  registration_code=
  rm -rf "$stage"
  exit "$result"
}
trap finish EXIT
trap 'exit 1' HUP INT TERM
fetch() { curl --fail --silent --show-error --proto '=https' --tlsv1.2 --connect-timeout 10 --max-time 120 --max-filesize 67108864 "$origin/agent/v1/releases/$1" -o "$stage/$1"; }
fetch SHA256SUMS
printf '%s  %s\n' "$release_digest" "$stage/SHA256SUMS" | sha256sum -c - >/dev/null || fail 'release_changed_generate_new_command'
for name in "hm-agent-linux-$arch" hm-agent.service; do
  fetch "$name"
  digest=$(awk -v name="$name" '$2==name {print $1}' "$stage/SHA256SUMS")
  case "$digest" in *[!a-f0-9]*|'') fail 'invalid_artifact_digest';; esac
  [ "${#digest}" = 64 ] || fail 'invalid_artifact_digest'
  printf '%s  %s\n' "$digest" "$stage/$name" | sha256sum -c - >/dev/null || fail 'artifact_checksum_mismatch'
done
chmod 0755 "$stage/hm-agent-linux-$arch"
# Check the downloaded executable before changing the installed service.
"$stage/hm-agent-linux-$arch" version >/dev/null
if ! getent passwd hm-agent >/dev/null; then useradd --system --user-group --home-dir "$state" --no-create-home --shell /usr/sbin/nologin hm-agent; fi
install -d -m 0755 "$cfg" /usr/local/lib/harbormaster
install -d -m 0700 -o hm-agent -g hm-agent "$state"
printf '%s\n' 'harbormaster-agent-v1' > "$managed"
if [ ! -f "$cfg/agent.key" ]; then openssl rand -base64 32 > "$cfg/agent.key"; fi
chmod 0600 "$cfg/agent.key"
mutated=1
if [ "$had_unit" = 1 ]; then systemctl stop hm-agent.service; fi
# Registration is a one-time elevated setup operation; runtime is unprivileged.
printf '%s\n' "$registration_code" | HM_MASTER_KEY_FILE="$cfg/agent.key" "$stage/hm-agent-linux-$arch" register --server "$origin" --registration-code-stdin
registration_code=
chown -R hm-agent:hm-agent "$state"
install -m 0755 "$stage/hm-agent-linux-$arch" "$binary"
install -m 0644 "$stage/hm-agent.service" "$unit"
printf 'HM_AGENT_INTERFACES=%s\nHM_AGENT_REPORT_INTERVAL=%s\n' "$interfaces" "$report_interval" > "$cfg/agent.env"
chmod 0644 "$cfg/agent.env"
systemctl daemon-reload
systemctl enable --now hm-agent.service
sleep 2
systemctl is-active --quiet hm-agent.service || fail 'agent_service_not_running'
success=1
printf '%s\n' 'agent_installed_check_panel_for_first_fresh_report'
