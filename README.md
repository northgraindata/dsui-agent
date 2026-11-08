# dsui-agent

`dsui-agent` gives a [DSUI](https://github.com/northgraindata/dsui) instance access to data sources in a private network.
It makes one outbound connection to DSUI and forwards traffic to the hosts you allow.
You open no inbound ports, and DSUI never gets a route into your network that you did not list.

It does not care how your stack is deployed.
It is a single static binary and a small container image.
It works the same next to Docker, in Kubernetes, on an ECS task, on a plain VM under systemd, or on a Windows server.

```
 your network                                   internet                DSUI instance
┌────────────────────────┐                                         ┌───────────────────┐
│ dsui-agent             │ ── outbound wss :443 (token) ─────────▶ │ tunnel endpoint   │
│  allowlist:            │ ◀───── streams for allowed targets ──── │                   │
│   airflow.internal:8080│                                         │ adapters (Airflow,│
│   clickhouse:8123      │                                         │ ClickHouse, ...)  │
└───────────┬────────────┘                                         └───────────────────┘
            ▼
   Airflow, ClickHouse, Postgres, a reverse proxy, ...
```

The agent forwards bytes and never reads them.
It does not touch HTTP headers, TLS, or authentication.
Your reverse proxy, Basic auth, JWT, mTLS, and database passwords keep working, and the credentials stay in DSUI.

One agent serves a whole site.
Many DSUI services can share it, each pointing at a different host and port that the agent can reach.

## How you use it

1. In DSUI open **Connectors** and create an agent. DSUI shows a token once and ready-made commands for your environment.
2. Run the agent next to your sources (examples below). Within seconds DSUI shows it as **online** together with the IP it connects from.
3. Add a service in DSUI, choose any adapter, and pick the connection method **Via DSUI Agent**.
   Select the agent and enter the host and port as the agent sees them, for example `airflow-api:8080`.
4. Press **Test connection**.

## Install

| Where | How |
| --- | --- |
| Docker | `docker run ghcr.io/northgraindata/dsui-agent` (see [examples/docker-compose.yml](examples/docker-compose.yml)) |
| Kubernetes | Helm chart `oci://ghcr.io/northgraindata/charts/dsui-agent` or [plain manifests](examples/kubernetes/manifest.yaml) |
| AWS ECS or EC2 | [Terraform example](examples/terraform-ecs/main.tf) |
| Linux VM | `.deb` or `.rpm` from the [releases](https://github.com/northgraindata/dsui-agent/releases), or the tarball with the [systemd unit](deploy/systemd/dsui-agent.service) |
| Windows | [Service with NSSM](examples/windows.md) |
| Linux, macOS | `curl -fsSL https://raw.githubusercontent.com/northgraindata/dsui-agent/main/install.sh \| sh` (verifies the checksum) |
| Other | archive or bare binary from the releases |

Every release lists its assets per platform (archives and bare binaries named `dsui-agent_<version>_<os>_<arch>`), checksums, a cosign signature of the checksums, and SBOMs.
The release notes show how to verify them.

## Examples

### Docker or Compose (also Coolify, Portainer, Dokploy)

```yaml
services:
  dsui-agent:
    image: ghcr.io/northgraindata/dsui-agent:latest
    restart: unless-stopped
    environment:
      DSUI_URL: https://dsui.example.com
      DSUI_TOKEN: ${DSUI_TOKEN}
      DSUI_AGENT_NAME: my-stack
      DSUI_TARGETS: airflow-api:8080,clickhouse:8123
```

Put the agent in the same network as the services.
Targets are the service names of that network.

### Kubernetes with Helm

```sh
kubectl create namespace dsui
kubectl -n dsui create secret generic dsui-agent-token --from-literal=token='<token from DSUI>'

helm install dsui-agent oci://ghcr.io/northgraindata/charts/dsui-agent -n dsui \
  --set dsui.url=https://dsui.example.com \
  --set token.existingSecret=dsui-agent-token \
  --set 'targets[0]=airflow-api.data.svc.cluster.local:8080' \
  --set 'targets[1]=clickhouse.data.svc.cluster.local:8123'
```

### Linux VM with systemd, no Docker

```sh
sudo dpkg -i dsui-agent_<version>_linux_amd64.deb      # or: sudo rpm -i ...
echo '<token from DSUI>' | sudo tee /etc/dsui-agent/token && sudo chmod 600 /etc/dsui-agent/token
sudoedit /etc/dsui-agent/agent.yaml                     # set url and targets
dsui-agent check --config /etc/dsui-agent/agent.yaml --token-file /etc/dsui-agent/token
sudo systemctl enable --now dsui-agent
```

### AWS: ECS Fargate and a private RDS

Use the [Terraform example](examples/terraform-ecs/main.tf).
It creates the task, the log group, and a security group.
Allow that security group on the database's inbound rules and list the RDS endpoint as a target.

### Behind a corporate proxy

The agent only needs to reach DSUI over HTTPS.
If that goes through a proxy, set the standard variables for the agent process:

```sh
HTTPS_PROXY=http://proxy.corp.local:3128
NO_PROXY=localhost,127.0.0.1
```

If the proxy re-signs TLS, pass its root certificate with `--ca-file /etc/ssl/corp-ca.pem`.
A target can be anything the agent host can reach, including an internal reverse proxy such as `https://airflow.corp.local` with its own login.
List it as `airflow.corp.local:443`, and enter the reverse proxy's credentials in DSUI as you would for a direct connection.

## Configuration

Everything can be set with a flag, an environment variable, or a YAML file.
Precedence: flag, then environment, then file.

| Flag | Environment | YAML key | Meaning |
| --- | --- | --- | --- |
| `--url` | `DSUI_URL` | `url` | DSUI instance URL |
| `--token-file` | `DSUI_TOKEN_FILE` | `token_file` | file with the agent token (preferred) |
| `--token` | `DSUI_TOKEN` | `token` | agent token |
| `--targets` | `DSUI_TARGETS` | `targets` | allowlist, comma separated in flags and env |
| `--name` | `DSUI_AGENT_NAME` | `name` | display name in DSUI |
| `--ca-file` | `DSUI_CA_FILE` | `ca_file` | extra CA bundle for reaching DSUI |
| `--log-format` | `DSUI_LOG_FORMAT` | `log_format` | `text` or `json` |
| `--log-level` | `DSUI_LOG_LEVEL` | `log_level` | `debug`, `info`, `warn`, `error` |
| `--config` | `DSUI_CONFIG` | | path to a YAML file |
| | `DSUI_MAX_STREAMS` | `max_streams` | concurrent streams, default 256 |

`dial_timeout` (default `10s`) and `heartbeat_interval` (default `30s`) are YAML only.

### Allowlist syntax

| Entry | Matches |
| --- | --- |
| `airflow.internal:8080` | that host and port |
| `db.internal:*` | any port on that host |
| `*.corp.local:443` | any subdomain of `corp.local` on port 443 |
| `10.20.0.0/16:5432` | IP addresses in that range, port 5432 |

The list is enforced by the agent.
DSUI cannot add to it.
An empty list is a configuration error.

## Commands

```
dsui-agent run      run the agent (default)
dsui-agent check    verify config, DSUI access, token and each target, then exit
dsui-agent version
```

In an interactive terminal, `run`, `check` and `help` open with a short start screen.
Anywhere else (Docker without `-t`, systemd, Kubernetes, CI), or with `NO_COLOR` or `--log-format json`, the output is the plain logs only.
Logs are always written; lines logged during the start screen are held and printed right after it.

`check` is the fastest way to debug a new install.
It prints one line per step and exits non-zero when something fails.

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| `DSUI refused this agent (unauthorized)` | token is wrong, revoked, or belongs to another instance |
| `HTTP 404 ... no connector support` | wrong URL, a path prefix is missing, or the DSUI version is too old |
| `connect ... x509` | a proxy re-signs TLS: use `--ca-file` |
| Agent shows online but a service fails | the host is not in `targets`, or the agent cannot reach it. Run `check` on the agent host |
| `not_allowed` in the DSUI test | the host and port DSUI used differ from the allowlist entry |
| frequent reconnects | a proxy or load balancer closes idle WebSockets. Keep idle timeouts above 60 seconds |

Run with `--log-level debug` to see every stream.

## Security model

- Outbound only. The agent has no listening port.
- The allowlist lives on your side. DSUI cannot reach anything else through the agent.
- Source credentials never pass through the agent configuration. The agent holds one token for its tunnel.
- Tokens can be revoked in DSUI. Revoked agents are refused on the next connect and disconnected.
- The container runs as non-root on a read-only filesystem with all capabilities dropped.
- Use a database account with read-only rights wherever DSUI only needs to read.

Report vulnerabilities as described in [SECURITY.md](SECURITY.md).

## Protocol

The wire protocol is documented in [docs/protocol.md](docs/protocol.md).

## Development

```sh
make test      # go vet + go test -race
make build     # bin/dsui-agent
make docker    # local image dsui-agent:local
```

Commits follow [Conventional Commits](https://www.conventionalcommits.org/).
Releases are automated: merging the release PR created by release-please tags the version, and GoReleaser publishes binaries, packages, the image, and the Helm chart.
`CHANGELOG.md` is generated and must not be edited by hand.

## License

Apache-2.0, see [LICENSE](LICENSE).
