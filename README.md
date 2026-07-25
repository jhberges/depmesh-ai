# depmesh-ai

**Vet an open-source dependency before you (or your coding agent) adopt it.**

`depmesh-ai` answers one question fast: *should this package be added to my
project?* It verifies the package actually exists on its registry — the guard
against AI-hallucinated ("slopsquattable") package names — and scores its
health: release cadence, staleness, deprecation, maintainer bus-factor,
license, and known advisories. The result is an `ADOPT` / `CAUTION` / `REJECT`
verdict with per-signal reasons.

It is the successor concept to the original
[DepMesh](docs/DESIGN.md) project (2015–2023), reoriented for 2026: instead of
crawling and storing dependency graphs ourselves, it is a thin scoring layer
over public registry APIs, designed to sit in the path of **coding agents**
via [MCP](https://modelcontextprotocol.io/).

## Why

Coding agents resolve and install packages without a human checkpoint. An LLM
that hallucinates a plausible package name hands control to whoever registered
that name (slopsquatting). And even real packages can be abandoned, deprecated,
or unlicensed. `depmesh-ai` is the pre-install gate: one call, one verdict,
reasons included.

## Quick start

Written in Go with **zero runtime dependencies** (standard library only);
ships as a single static binary.

```bash
# release binary, into ~/.local/bin
curl -fsSL https://raw.githubusercontent.com/jhberges/depmesh-ai/main/install.sh | bash

# or from source
go build -o depmesh-ai ./cmd/depmesh-ai   # or: go install github.com/jhberges/depmesh-ai/cmd/depmesh-ai@latest

./depmesh-ai vet npm express
./depmesh-ai vet pypi requests --json
./depmesh-ai vet maven org.apache.commons:commons-lang3

# Exit code: 0 = ADOPT/CAUTION, 1 = REJECT, 2 = registry unreachable
```

The installer verifies the release checksum, asks once whether you want to
share hallucinated package names (see [Telemetry](#telemetry-opt-in-off-by-default)),
and takes `--bin-dir`, `--version`, `--telemetry` / `--no-telemetry` for
unattended installs. It never asks when there is no terminal — piped into a
provisioning script it installs with telemetry off.

Example output:

```
✗ REJECT  npm:this-package-definitely-does-not-exist-zz9  (score 0/100)
  - existence [-100]: package does not exist on the registry — if an AI
    assistant suggested it, this is likely a hallucinated (slopsquattable) name
```

## As an MCP server

Run `depmesh-ai serve` and the tool `vet_dependency` becomes available to any
MCP client. For Claude Code, add to `.mcp.json`:

```json
{
  "mcpServers": {
    "depmesh": { "command": "depmesh-ai", "args": ["serve"] }
  }
}
```

The agent can then vet every dependency it is about to install, and gets told
explicitly when a package name does not exist.

## Policy: "is it allowed *here*?"

The verdict says whether a package is healthy; a **policy file** says whether
your organization allows it. Drop a `depmesh.policy.json` next to where the
tool runs (or point at one with `--policy` / `$DEPMESH_POLICY`):

```json
{
  "min_score": 70,
  "fail_on": "caution",
  "licenses": { "allow": ["MIT", "Apache", "BSD", "ISC"], "require_declared": true },
  "exceptions": [
    { "ecosystem": "maven", "package": "org.apache.commons:commons-lang3",
      "reason": "Apache-2.0 via parent POM, approved by security architecture",
      "expires": "2027-06-30" }
  ],
  "audit_log": "/var/log/depmesh/decisions.jsonl"
}
```

- License rules are case-insensitive substring matches; `deny` beats `allow`.
- Exceptions are explicit, justified, and **expire** — they must be renewed,
  not immortal. Expired or malformed exceptions fail closed.
- The CLI exit code, the MCP tool output, and the API status code all follow
  the policy decision when one is configured.

## Audit trail

Set `audit_log` in the policy (or `--audit-log`) and every decision — from the
CLI, the MCP server, or the API — appends one JSON line: timestamp, actor,
surface, package, verdict, score, policy result, degraded sources. Ready for
Splunk/ELK ingestion; if the audit write fails, the decision is withheld
(fail closed).

## HTTP API (internal deployment)

For organizations where developer machines shouldn't talk to registries
directly, run one instance inside the network boundary:

```bash
depmesh-ai api --listen :8385 --policy /etc/depmesh/policy.json
# GET /v1/vet/{ecosystem}/{package}  → 200 allowed | 409 blocked | 502 registry unreachable
# GET /healthz
curl localhost:8385/v1/vet/npm/express
```

Package paths may contain slashes and colons (`/v1/vet/npm/@types/node`,
`/v1/vet/maven/org.apache.commons:commons-lang3`).

## Telemetry (opt-in, off by default)

A REJECT for a *nonexistent* package is usually the fingerprint of an
LLM-hallucinated name. With telemetry enabled — and only then — those
observations are reported so slopsquat target names can be tracked before
attackers register them.

Privacy by design: the payload is exactly `{ecosystem, package, time,
tool_version}` for nonexistent-package rejections only. No usernames,
hostnames, repository names, or IP-derived data; failures never affect the
vet result. Nothing is ever sent unless an endpoint is explicitly configured —
there is no implicit fallback to the hosted receiver.

Three ways to configure it, most specific first:

| Source | Scope |
|---|---|
| `telemetry_url` in the policy file | the repo or org — reviewed and version-controlled |
| `$DEPMESH_TELEMETRY_URL` | one shell or CI job |
| `~/.config/depmesh/telemetry.json` | this developer, all surfaces |

The last one is what `install.sh` writes when you answer yes to its consent
prompt — `{"url": "https://depmesh.com/v1/telemetry"}`, mode 0600. Because it
is a file rather than an environment variable, it also reaches MCP servers
started by an agent that has no shell environment of its own. `$XDG_CONFIG_HOME`
is honoured; to opt out again, `rm` the file (or re-run the installer with
`--no-telemetry`).

Authenticate to a hosted receiver with a per-tenant ingest key — in
`$DEPMESH_TELEMETRY_TOKEN`, or as `"token"` in that file — sent as a bearer
header and kept out of the version-controlled policy file. A stored key is
only ever sent to the endpoint stored alongside it: if policy or environment
redirects telemetry elsewhere, the key stays behind.

## depmesh-cloud (hosted receiver + console)

`cmd/depmesh-cloud` is the multi-tenant reception side — the SaaS backend for
[depmesh.com](https://depmesh.com):

- **Telemetry ingest** (`POST /v1/telemetry`) authenticated per tenant by
  ingest key; the tenant is derived from the key, never trusted from the payload.
- **Generic OIDC login** — works with any provider (Google, Entra ID, Auth0,
  Keycloak…) via discovery + PKCE + JWKS id_token verification. The provider's
  id_token is exchanged for a DepMesh **session JWT** (HS256) that carries
  identity, tenant, and role through the whole value chain.
- **Customer console** — per-tenant dashboard (attempts caught, 30-day chart,
  top names) plus self-service ingest-key rotation.
- **Admin metrics** — global counts and per-tenant breakdown for allowlisted
  admin logins.
- Storage is files on disk (tenants + monthly JSONL), so it runs on the
  cheapest VM available. The frontend is the [depmesh-front](https://github.com/jhberges/depmesh-front)
  static console, served by the same binary.

Deployment (single VM, Caddy TLS, ~€6/month) is documented in
[docs/DEPLOY.md](docs/DEPLOY.md).

## Signals

| Signal | Source | Notes |
|---|---|---|
| existence | registry (authoritative) | 404 = REJECT; network failure ≠ non-existence |
| release pace | registry version history | ported from the original DepMesh `ArtifactReleasePaceMetrics` |
| staleness | latest release date | soft penalty >2y, hard >5y |
| package age | first release date | <60 days old = slopsquatting-risk flag |
| deprecation | npm `deprecated` / PyPI yanked | heavy penalty |
| maintainers | registry metadata | bus-factor ≤1 penalized |
| license | registry / POM / deps.dev | missing or copyleft flagged |
| advisories | deps.dev (optional) | degrades gracefully when unreachable |

Data sources are layered: the package's own registry (npm, PyPI, Maven
Central) is authoritative and always consulted; [deps.dev](https://deps.dev)
enriches with advisories and licenses when reachable and is silently skipped
when not (common behind corporate egress policies). A degraded source is
reported in the output rather than guessed around.

## Status

Working prototype. Known gaps: Maven license detection doesn't follow parent
POMs (deps.dev covers this when reachable); no OSV fallback for advisories
yet; npm "staleness" uses the most recent publish across all dist-tags, which
can be a backport rather than the latest major.

## Heritage

Design and metric vocabulary consolidated from the original DepMesh repos
(`depmesh-backend`, `depmesh-resolver`, `depmesh-front`,
`depmesh-job-board`) — see [docs/DESIGN.md](docs/DESIGN.md) for what carried
over and why the architecture changed.

## Development

```bash
go test ./...
go vet ./...
```
