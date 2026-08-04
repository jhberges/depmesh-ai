# Security policy

## Reporting a vulnerability

**Please do not open a public issue.** Issues are visible to everyone, so
reporting a vulnerability there discloses it to the world before there is a fix
— which for a tool people run against their own supply chain is the worst
possible outcome.

Use GitHub's private vulnerability reporting instead:

**[→ Report a vulnerability](https://github.com/jhberges/depmesh-ai/security/advisories/new)**

You can also reach it from the **Security** tab of this repository via the
*Report a vulnerability* button. It opens a private advisory visible only to
the maintainers; you do not need to email anyone or find an address.

Please include enough to reproduce it: the version (`depmesh-ai version`), the
platform, and the smallest input that triggers the behaviour.

### What to expect

- **Acknowledgement within seven days.** This is a small project with one
  maintainer, so that is a realistic floor rather than an aspiration.
- An assessment of whether it is exploitable and how severe it looks, with the
  reasoning, so you can disagree.
- Credit in the advisory unless you would rather not be named.

No fixed remediation deadline is promised, because one that gets missed is
worse than none. Severity drives the timeline, and you will be told what it is.

## Scope

This repository is the `depmesh-ai` MCP server and CLI. In scope: anything that
lets input change what the tool decides, what it executes, or what it discloses.
Some examples, not a limit:

- A crafted registry response that causes a package to be scored ADOPT when it
  should be REJECT, or that bypasses a policy rule.
- Command or path injection through a package name, ecosystem, or policy file.
- The audit trail being silently incomplete — a decision that is made but not
  logged. The tool's value in a regulated setting depends on that log.
- Telemetry sending anything beyond the documented payload, or sending it to an
  endpoint the user did not configure. See
  [docs/TELEMETRY-PROTOCOL.md](docs/TELEMETRY-PROTOCOL.md).
- The installers (`scripts/install.sh`, `scripts/install.ps1`) writing outside
  the install directory, or being trivially tricked into installing something
  other than the verified release artifact.

**Out of scope:** vulnerabilities in the packages `depmesh-ai` reports on — it
is a measuring instrument, and a REJECT verdict is the tool working. Also out
of scope: the hosted service at depmesh.com, which is a separate system; report
those through the same private channel and they will be routed.

## Supported versions

The latest release only. This is early software — there is no long-term support
branch, and backporting to older tags is not offered. Fixes ship in a new
release.

## A note on dependencies

`depmesh-ai` has **no external dependencies**: standard library only, no
`go.sum`, nothing vendored. CI fails the build if that ever stops being true.
So the dependency-related attack surface of this tool is its own source and the
Go toolchain, and nothing else — which is deliberate, given what it does.
