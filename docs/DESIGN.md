# Design & heritage: from DepMesh to depmesh-ai

## The original DepMesh (2015–2023)

DepMesh set out to track dependencies and the risks and quality metrics
induced by OSS components, prototyped on Maven with the intent of being
package-system agnostic. It was built as four repos:

| Repo | Role | Stack |
|---|---|---|
| `depmesh-backend` | Dependency graph + metrics store, GitHub probe, REST API | Gradle multi-module, Spring Boot 2.5, Neo4j, Avro, Redis |
| `depmesh-resolver` | Resolved Maven dependency trees by driving a local Maven install, message-driven | Spring Boot 2.7, RabbitMQ |
| `depmesh-front` | UI | React 0.13, Grunt/Bower (2015) |
| `depmesh-job-board` | K8s "tear-off flyer" job scheduling | skeleton only |

## Why the architecture changed

By 2026 the heavy parts of DepMesh are commodities:

- **Dependency resolution** (the resolver's whole job) is served free by
  Google's [deps.dev](https://deps.dev) API for Maven, npm, PyPI, Go, Cargo,
  and NuGet — no local Maven, no message queue, no worker fleet.
- **Repo health signals** (the ALM-probe's job) are standardized by
  [OpenSSF Scorecard](https://scorecard.dev/) and republished through deps.dev.
- **Org-wide inventory + policy** is owned by OWASP Dependency-Track
  (free, 20k+ orgs), and the commercial SCA space (Snyk, Socket, Endor Labs,
  Black Duck, Mend) is consolidated and venture-funded.

What is *not* well served in 2026 is the **moment of adoption** — especially
when the adopter is a coding agent. LLMs hallucinate package names at
measurable rates; attackers register those names (slopsquatting); agentic
workflows install packages with no human review step. Snyk sunset its public
Advisor package-health page in January 2026, removing one of the few
lightweight "should I use this?" surfaces.

So depmesh-ai inverts the original architecture: **no storage, no crawling,
no graph database — a stateless scoring layer over public APIs, exposed as a
CLI and an MCP tool** so it can sit directly in an agent's install path.

## What carried over

The metric *vocabulary* survived; the code did not (by design).

| Legacy class (`com.depmesh.backend.model`) | depmesh-ai descendant | Notes |
|---|---|---|
| `Artifact`, `Version`, `VersionRef` | `PackageFacts`, `ReleaseRef` (`model.py`) | Same newest-first release-list contract |
| `ArtifactReleasePaceMetrics` (+ builder, `PaceState`) | `metrics.py` | Direct port; the original unit-test fixtures (one-week / two-day averages) pass unchanged. The port fixes a seconds-vs-milliseconds unit bug in the Java `calculateAveragePace` (`toEpochSecond` fed into `Duration.ofMillis`). |
| `AlmIssueDigest` (submitter/assignee concentration, open-duration stats) | `maintainers` signal (bus-factor) | The full issue-digest idea maps onto OpenSSF Scorecard's `Maintained` check; a future enrichment source rather than something we compute |
| `AggregatedLicenseOverview`, `ReferencedLicense` | `license` signal | Single-package scope for now; scoped/transitive aggregation returns if a graph view comes back |
| `ArtifactAccumulatedMetrics`, `VersionSizeMetric` | not ported | Size/weight metrics are a candidate future signal (deps.dev exposes dependency counts) |
| `depmesh-resolver` (Maven-in-a-box) | `sources/maven.py` + deps.dev | Replaced by reading Maven Central + deps.dev directly |
| Avro/RabbitMQ messaging, Neo4j graph | dropped | No persistence layer in a stateless vet call |

## Architecture

```
cmd/depmesh-ai (CLI) ──┐
                       ├── internal/vet (verdict engine: signals → score → ADOPT/CAUTION/REJECT)
internal/mcp (stdio) ──┘         │
                                 ├── internal/metrics (release pace, ported from DepMesh)
                                 └── internal/sources/
                                     ├── npm.go     ─ registry.npmjs.org  (authoritative)
                                     ├── pypi.go    ─ pypi.org            (authoritative)
                                     ├── maven.go   ─ repo1.maven.org     (authoritative)
                                     └── depsdev.go ─ api.deps.dev        (optional enrichment)
```

**Language: Go.** An initial prototype was written in Python to validate the
design; it was rewritten in Go (same structure, same signals, same ported
test fixtures) because the tool's shape favors it: a single static binary is
the easiest thing to drop into a developer machine or CI image, the standard
library covers HTTP/JSON/XML/regexp so the zero-dependency rule holds, and a
CLI/MCP server benefits from instant startup.

Design rules:

1. **The registry is authoritative for existence.** Only an HTTP 404 from the
   package's own registry may set `exists=False`. A network/policy failure is
   `exists=None` and is reported as a degraded source — an outage must never
   look like "package doesn't exist" (or vice versa).
2. **Enrichment only adds, never replaces.** deps.dev fills advisories and
   licenses when reachable; when blocked (common behind corporate egress
   policies — it was blocked in the environment this was built in), signals
   that depend on it read "unknown", not "fine".
3. **Zero runtime dependencies.** A dependency-vetting tool that pulls in a
   dependency tree of its own would be a punchline. Stdlib only; the MCP
   server is a hand-rolled JSON-RPC 2.0 stdio loop (initialize, tools/list,
   tools/call, ping).
4. **Reasons, not just scores.** Every signal carries a sentence a human or an
   agent can act on.

## Future directions

- OSV (`api.osv.dev`) as an advisory fallback when deps.dev is unreachable.
- OpenSSF Scorecard signals (Maintained, Security-Policy) via deps.dev
  project lookup — the modern home of the old ALM-probe idea.
- Name-similarity check against top-N popular packages (typosquat distance).
- More ecosystems: Cargo, Go, NuGet (deps.dev covers all of them).
- A `vet-manifest` mode: score every dependency in a `package.json` /
  `pom.xml` / `pyproject.toml` in one call.
