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

## Install

Written in Go with **zero dependencies** — standard library only, no `go.sum`,
nothing to audit but this repository. A tool that exists to police your supply
chain should not enlarge it, so CI fails the build if a single external import
appears. Ships as one static binary.

The installers download it, verify its checksum, and register the MCP server
**globally** with every coding CLI and IDE they find (see
[As an MCP server](#as-an-mcp-server)):

```bash
# Linux / macOS
curl -fsSL https://github.com/jhberges/depmesh-ai/releases/latest/download/install.sh | bash
```

```powershell
# Windows
irm https://github.com/jhberges/depmesh-ai/releases/latest/download/install.ps1 | iex
```

They prompt for the install location and for each client before touching
anything, and they never overwrite an existing config without leaving a
`.depmesh.bak` beside it. Both accept flags for unattended runs:

```bash
curl -fsSL .../install.sh | bash -s -- --yes --dir ~/.local/bin --clients claude,copilot
```

| Flag | PowerShell | Meaning |
|---|---|---|
| `--dir DIR` | `-InstallDir` | where the binary goes |
| `--version TAG` | `-Version` | release to install (default: latest) |
| `--policy FILE` | `-Policy` | org policy file to pin into every client config |
| `--clients LIST` | `-Clients` | `claude,copilot,codex,cursor,vscode` \| `all` \| `none` |
| `--yes` | `-Yes` | accept every default, never prompt |
| `--no-configure` | `-NoConfigure` | install the binary only |
| `--telemetry` | `-Telemetry` | opt in without being asked |
| `--no-telemetry` | `-NoTelemetry` | opt out without being asked |
| `--telemetry-url URL` | `-TelemetryUrl` | receiver endpoint |
| `--telemetry-token KEY` | `-TelemetryToken` | per-tenant ingest key |

The two scripts are kept option-for-option identical, and CI fails if they
drift — see `scripts/check-installer-parity.sh`. Telemetry is only ever
switched on by an explicit answer or an explicit flag: an unattended run
without one of the telemetry flags leaves it exactly as it found it.

Set `$GITHUB_TOKEN` when the repository is private, or
`$DEPMESH_DOWNLOAD_BASE` to pull the assets from an internal mirror. The
installers are themselves release assets listed in `checksums.txt`, so they can
be verified before being piped to a shell.

Without the installer:

```bash
go build -o depmesh-ai ./cmd/depmesh-ai   # or: go install github.com/jhberges/depmesh-ai/cmd/depmesh-ai@latest
```

## Quick start

```bash
depmesh-ai vet npm express
depmesh-ai vet pypi requests --json
depmesh-ai vet maven org.apache.commons:commons-lang3

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
MCP client. The agent can then vet every dependency it is about to install, and
gets told explicitly when a package name does not exist.

For one project, drop this in `.mcp.json` at the repo root:

```json
{
  "mcpServers": {
    "depmesh": { "command": "depmesh-ai", "args": ["serve"] }
  }
}
```

### Global setup

A pre-install gate that only covers the repo you remembered to configure isn't
much of a gate. Nothing in `serve` is project-scoped, so register it once per
machine instead — `scripts/install.sh` and `scripts/install.ps1` do exactly
this, and here is what they write:

| Client | Global registration |
|---|---|
| Claude Code | `claude mcp add depmesh --scope user -- /usr/local/bin/depmesh-ai serve` |
| GitHub Copilot CLI | `~/.copilot/mcp-config.json` (`$COPILOT_HOME` moves it) |
| Codex CLI | `codex mcp add depmesh -- /usr/local/bin/depmesh-ai serve` |
| Cursor | `~/.cursor/mcp.json` |
| VS Code | `code --add-mcp '{"name":"depmesh","command":"/usr/local/bin/depmesh-ai","args":["serve"]}'` |

Claude Code's `--scope user` is the difference between "this project" and
"every project"; the default scope is `local`. Copilot names stdio servers
`local` and hides the tool unless `tools` is set:

```json
{
  "mcpServers": {
    "depmesh": {
      "type": "local",
      "command": "/usr/local/bin/depmesh-ai",
      "args": ["serve"],
      "tools": ["*"]
    }
  }
}
```

Three things worth knowing before you register it by hand:

- **Use an absolute path.** An MCP client inherits its own environment, not
  your shell's, and `go install` drops the binary in `~/go/bin` — frequently
  not on the PATH of a desktop-launched agent. A bare `depmesh-ai` is the usual
  cause of a global server that silently fails to start.
- **Policy discovery follows the client, not the project.** With no explicit
  path, `./depmesh.policy.json` is resolved against the working directory the
  client launched the server in, and it is read once at startup. That gives
  per-repo policy for free, but for one org-wide file pin it instead — pass
  `--policy /etc/depmesh/policy.json` in `args`, or set `DEPMESH_POLICY` in the
  server's `env` (`--policy` / `-Policy` in the installers does this for you).
- **Keep `audit_log` absolute** for the same reason. An audit write that fails
  withholds the decision, so a relative path that lands somewhere unwritable
  turns every vet into an error rather than a verdict.

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

The last one is what the installer writes when you answer yes to its consent
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

### Where reports go

Nowhere, unless you point them somewhere. The receiver is whatever URL you
configure — run your own, or use the hosted one at
[depmesh.com](https://depmesh.com). The request format is one small documented
`POST`, specified in [docs/TELEMETRY-PROTOCOL.md](docs/TELEMETRY-PROTOCOL.md),
so an internal endpoint is a few lines of code to implement.

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
