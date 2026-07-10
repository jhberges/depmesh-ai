"""npm registry source (registry.npmjs.org)."""
from __future__ import annotations

import datetime as dt
import urllib.parse

from ..model import Ecosystem, PackageFacts, ReleaseRef
from .http import NotFound, get_json

REGISTRY = "https://registry.npmjs.org"


def _parse_date(value: str) -> dt.date | None:
    try:
        return dt.datetime.fromisoformat(value.replace("Z", "+00:00")).date()
    except ValueError:
        return None


def fetch(name: str) -> PackageFacts:
    facts = PackageFacts(Ecosystem.NPM, name)
    quoted = urllib.parse.quote(name, safe="@")
    try:
        doc = get_json(f"{REGISTRY}/{quoted}")
    except NotFound:
        facts.exists = False
        return facts

    facts.exists = True
    facts.description = doc.get("description")
    facts.latest_version = (doc.get("dist-tags") or {}).get("latest")

    times = doc.get("time") or {}
    versions = doc.get("versions") or {}
    releases = [
        ReleaseRef(version, _parse_date(stamp))
        for version, stamp in times.items()
        if version not in ("created", "modified")
    ]
    releases.sort(key=lambda r: (r.release_date or dt.date.min), reverse=True)
    facts.releases = releases

    latest = versions.get(facts.latest_version) or {}
    license_field = latest.get("license") or doc.get("license")
    if isinstance(license_field, dict):
        license_field = license_field.get("type")
    facts.license = license_field
    if latest.get("deprecated"):
        facts.deprecated = True
        facts.deprecation_message = str(latest["deprecated"])

    maintainers = doc.get("maintainers")
    if isinstance(maintainers, list):
        facts.maintainer_count = len(maintainers)

    repo = doc.get("repository")
    if isinstance(repo, dict):
        facts.repository_url = repo.get("url")
    elif isinstance(repo, str):
        facts.repository_url = repo
    facts.homepage = doc.get("homepage")
    return facts
