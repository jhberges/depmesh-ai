"""PyPI source (pypi.org JSON API)."""
from __future__ import annotations

import datetime as dt
import urllib.parse

from ..model import Ecosystem, PackageFacts, ReleaseRef
from .http import NotFound, get_json

REGISTRY = "https://pypi.org/pypi"


def _release_date(files: list) -> dt.date | None:
    dates = []
    for f in files:
        stamp = f.get("upload_time_iso_8601") or f.get("upload_time")
        if stamp:
            try:
                dates.append(
                    dt.datetime.fromisoformat(stamp.replace("Z", "+00:00")).date()
                )
            except ValueError:
                pass
    return min(dates) if dates else None


def fetch(name: str) -> PackageFacts:
    facts = PackageFacts(Ecosystem.PYPI, name)
    try:
        doc = get_json(f"{REGISTRY}/{urllib.parse.quote(name)}/json")
    except NotFound:
        facts.exists = False
        return facts

    facts.exists = True
    info = doc.get("info") or {}
    facts.description = info.get("summary")
    facts.latest_version = info.get("version")
    facts.homepage = info.get("home_page") or (info.get("project_urls") or {}).get(
        "Homepage"
    )
    facts.repository_url = (info.get("project_urls") or {}).get(
        "Source"
    ) or (info.get("project_urls") or {}).get("Repository")

    license_field = info.get("license_expression") or info.get("license")
    if not license_field:
        for classifier in info.get("classifiers") or []:
            if classifier.startswith("License ::"):
                license_field = classifier.rsplit("::", 1)[-1].strip()
                break
    # Some projects paste the entire license text into the license field.
    if license_field and len(license_field) > 120:
        license_field = license_field.splitlines()[0][:120]
    facts.license = license_field

    releases = []
    yanked_latest = False
    for version, files in (doc.get("releases") or {}).items():
        releases.append(ReleaseRef(version, _release_date(files or [])))
        if version == facts.latest_version and files:
            yanked_latest = all(f.get("yanked") for f in files)
    releases.sort(key=lambda r: (r.release_date or dt.date.min), reverse=True)
    facts.releases = releases
    if yanked_latest:
        facts.deprecated = True
        facts.deprecation_message = "latest release is yanked on PyPI"
    return facts
