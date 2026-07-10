"""Command-line interface.

    python -m depmesh_ai vet npm express
    python -m depmesh_ai vet maven org.apache.commons:commons-lang3 --json
    python -m depmesh_ai serve          # MCP server on stdio
"""
from __future__ import annotations

import argparse
import sys

from .sources.http import SourceUnavailable
from .vet import Advice, Verdict, vet

_BADGE = {Advice.ADOPT: "✓", Advice.CAUTION: "⚠", Advice.REJECT: "✗"}


def _render(verdict: Verdict) -> str:
    lines = [
        f"{_BADGE[verdict.advice]} {verdict.advice.value}  "
        f"{verdict.ecosystem}:{verdict.package}  (score {verdict.score}/100)",
    ]
    if verdict.facts and verdict.facts.latest_version:
        lines.append(f"  latest: {verdict.facts.latest_version}")
    for signal in verdict.signals:
        marker = "-" if signal.delta < 0 else "·"
        delta = f" [{signal.delta:+d}]" if signal.delta else ""
        lines.append(f"  {marker} {signal.name}{delta}: {signal.reason}")
    for degraded in verdict.degraded:
        lines.append(f"  ? unavailable source: {degraded}")
    return "\n".join(lines)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="depmesh-ai",
        description="Vet an open-source dependency before adopting it.",
    )
    subcommands = parser.add_subparsers(dest="command", required=True)

    vet_cmd = subcommands.add_parser("vet", help="score a package")
    vet_cmd.add_argument("ecosystem", help="npm | pypi | maven")
    vet_cmd.add_argument("package", help="package name (maven: groupId:artifactId)")
    vet_cmd.add_argument("--json", action="store_true", help="machine-readable output")
    vet_cmd.add_argument(
        "--no-enrich",
        action="store_true",
        help="skip deps.dev enrichment (advisories)",
    )

    subcommands.add_parser("serve", help="run the MCP server on stdio")

    args = parser.parse_args(argv)

    if args.command == "serve":
        from .mcp_server import serve

        serve()
        return 0

    try:
        verdict = vet(args.ecosystem, args.package, enrich=not args.no_enrich)
    except ValueError as err:
        parser.error(str(err))
    except SourceUnavailable as err:
        print(f"error: registry unreachable, cannot vet: {err}", file=sys.stderr)
        return 2

    print(verdict.to_json() if args.json else _render(verdict))
    return 0 if verdict.advice is not Advice.REJECT else 1


if __name__ == "__main__":
    sys.exit(main())
