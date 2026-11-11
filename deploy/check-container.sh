#!/bin/sh
# Local/CI smoke check using fresh disposable credentials and an empty database.
# Requires a Linux Docker daemon and sudo for non-root bind-mount ownership.
set -eu
umask 077
image=${1:-harbormaster-server:local}
for tool in docker openssl curl python3; do
  command -v "$tool" >/dev/null 2>&1 || { printf '%s\n' "missing_dependency_$tool" >&2; exit 1; }
done
[ "$(docker info --format '{{.OSType}}')" = linux ] || { printf '%s\n' 'linux_docker_required' >&2; exit 1; }
[ "$(docker image inspect "$image" --format '{{.Config.User}}')" = '65532:65532' ] || { printf '%s\n' 'image_must_default_to_nonroot' >&2; exit 1; }
fixture=$(mktemp -d)
container=
cleanup() {
  status=$?
  trap - EXIT HUP INT TERM
  if [ -n "$container" ]; then docker rm -f "$container" >/dev/null 2>&1 || :; fi
  sudo rm -rf "$fixture"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
mkdir "$fixture/data"
openssl rand -base64 32 > "$fixture/master.key"
openssl rand -hex 32 > "$fixture/setup.token"
sudo chown 65532:65532 "$fixture/data" "$fixture/master.key" "$fixture/setup.token"
port=$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(('127.0.0.1', 0))
    print(sock.getsockname()[1])
PY
)
cat > "$fixture/config.json" <<EOF_CONFIG
{"listen":"127.0.0.1:$port","data_dir":"/var/lib/harbormaster-server","public_url":"https://panel.example","frontend":"api","trusted_proxies":["127.0.0.1/32"]}
EOF_CONFIG
chmod 0644 "$fixture/config.json"
container=$(docker run --detach --network host --read-only --cap-drop ALL --security-opt no-new-privileges:true \
  --mount "type=bind,source=$fixture/data,target=/var/lib/harbormaster-server" \
  --mount "type=bind,source=$fixture/config.json,target=/etc/harbormaster/server.json,readonly" \
  --mount "type=bind,source=$fixture/master.key,target=/run/secrets/master.key,readonly" \
  --mount "type=bind,source=$fixture/setup.token,target=/run/secrets/setup.token,readonly" \
  --env HM_MASTER_KEY_FILE=/run/secrets/master.key --env HM_SETUP_TOKEN_FILE=/run/secrets/setup.token \
  "$image")
ready=0
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  if docker exec "$container" /hm-server healthcheck --listen "127.0.0.1:$port" >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
[ "$ready" = 1 ] || { printf '%s\n' 'container_not_ready' >&2; exit 1; }
status=$(curl --silent --output "$fixture/status.json" --write-out '%{http_code}' \
  -H 'Host: panel.example' -H 'X-Forwarded-Proto: https' -H 'X-Forwarded-For: 192.0.2.5' \
  "http://127.0.0.1:$port/api/v1/setup/status")
[ "$status" = 200 ] || { printf '%s\n' 'proxy_setup_status_failed' >&2; exit 1; }
python3 - "$fixture/status.json" <<'PY'
import json,sys
with open(sys.argv[1]) as stream:
    assert json.load(stream)['initialized'] is False
PY
status=$(curl --silent --output /dev/null --write-out '%{http_code}' -H 'Host: panel.example' "http://127.0.0.1:$port/api/v1/setup/status")
[ "$status" = 403 ] || { printf '%s\n' 'plain_http_panel_was_accepted' >&2; exit 1; }
docker logs "$container" > "$fixture/logs" 2>&1
if sudo grep -Fq -f "$fixture/master.key" "$fixture/logs" || sudo grep -Fq -f "$fixture/setup.token" "$fixture/logs"; then
  printf '%s\n' 'container_logs_leaked_fixture_credentials' >&2; exit 1
fi
docker cp "$container:/etc/ssl/certs/ca-certificates.crt" "$fixture/ca.pem" >/dev/null
[ -s "$fixture/ca.pem" ] || { printf '%s\n' 'container_missing_ca_bundle' >&2; exit 1; }
printf '%s\n' 'container_nonroot_ready_proxy_headers_verified_secrets_not_logged'
