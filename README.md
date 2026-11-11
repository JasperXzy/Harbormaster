# Harbormaster

Monitoring and quota-guard tool for Alibaba Cloud ECS and Simple Application
Server instances across multiple accounts: CDT traffic-quota tracking with
real-time estimation, automatic instance shutdown before the quota is
exceeded, scheduled start/stop, keep-alive, alerts via Telegram, and latency
monitoring.

Only Alibaba Cloud is supported. Non-Alibaba machines cannot be added.

Components (all Go):

- `hm-server` — dashboard, storage, Alibaba Cloud API queries, alerts, automation
- `hm-agent` — runs on each managed Alibaba Cloud instance; reports public
  inbound/outbound traffic every 10 seconds and runs latency probes

See [docs/PRD.md](docs/PRD.md) for the full product requirements.
