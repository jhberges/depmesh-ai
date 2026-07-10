"""Optional enrichment from deps.dev (Google Open Source Insights).

deps.dev aggregates advisories, licenses, and OpenSSF Scorecard signals
for 50M+ package versions. It is the preferred advisory source, but many
corporate egress policies block it — enrichment therefore degrades
gracefully and only ever *adds* to registry facts, never replaces them.
"""
from __future__ import annotations

from ..model import Ecosystem, PackageFacts
from .http import NotFound, SourceUnavailable, get_json

API = "https://api.deps.dev/v3"

_SYSTEM = {
    Ecosystem.NPM: "npm",
    Ecosystem.PYPI: "pypi",
    Ecosystem.MAVEN: "maven",
}


def enrich(facts: PackageFacts) -> None:
    if not facts.exists or not facts.latest_version:
        return
    system = _SYSTEM[facts.ecosystem]
    name = facts.name.replace(":", "%3A").replace("/", "%2F")
    url = f"{API}/systems/{system}/packages/{name}/versions/{facts.latest_version}"
    try:
        doc = get_json(url)
    except (NotFound, SourceUnavailable) as err:
        facts.degraded.append(f"deps.dev ({err.__class__.__name__})")
        return

    advisories = doc.get("advisoryKeys") or []
    facts.advisory_count = len(advisories)
    if not facts.license:
        licenses = doc.get("licenses") or []
        if licenses:
            facts.license = ", ".join(licenses)
