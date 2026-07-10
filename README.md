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
go build -o depmesh-ai ./cmd/depmesh-ai   # or: go install github.com/jhberges/depmesh-ai/cmd/depmesh-ai@latest

./depmesh-ai vet npm express
./depmesh-ai vet pypi requests --json
./depmesh-ai vet maven org.apache.commons:commons-lang3

# Exit code: 0 = ADOPT/CAUTION, 1 = REJECT, 2 = registry unreachable
```

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
