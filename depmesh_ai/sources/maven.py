"""Maven Central source (repo1.maven.org).

Package names use the ``groupId:artifactId`` form, as in the original
DepMesh Maven prototype. This talks to the repository directly instead of
spawning a local Maven process the way depmesh-resolver did — version
lists come from maven-metadata.xml, release dates from the directory
listing, and the license from the latest POM.
"""
from __future__ import annotations

import datetime as dt
import re
import xml.etree.ElementTree as ET

from ..model import Ecosystem, PackageFacts, ReleaseRef
from .http import NotFound, SourceUnavailable, get_text

REPO = "https://repo1.maven.org/maven2"

# Directory listing lines look like:
#   <a href="3.12.0/" title="3.12.0/">3.12.0/</a>  2021-03-01 07:38  -
_LISTING_ROW = re.compile(
    r'href="(?P<version>[^"/]+)/"[^>]*>[^<]+</a>\s+(?P<date>\d{4}-\d{2}-\d{2})'
)


def _coordinates(name: str) -> tuple[str, str]:
    if ":" not in name:
        raise ValueError(
            f"maven package names must be 'groupId:artifactId', got {name!r}"
        )
    group, artifact = name.split(":", 1)
    return group, artifact


def _listing_dates(base_url: str) -> dict[str, dt.date]:
    try:
        listing = get_text(f"{base_url}/")
    except (NotFound, SourceUnavailable):
        return {}
    dates = {}
    for match in _LISTING_ROW.finditer(listing):
        try:
            dates[match.group("version")] = dt.date.fromisoformat(match.group("date"))
        except ValueError:
            pass
    return dates


def _pom_license(base_url: str, artifact: str, version: str) -> str | None:
    try:
        pom = get_text(f"{base_url}/{version}/{artifact}-{version}.pom")
    except (NotFound, SourceUnavailable):
        return None
    # Strip the default-namespace prefix so find() paths stay simple.
    pom = re.sub(r'xmlns="[^"]+"', "", pom, count=1)
    try:
        root = ET.fromstring(pom)
    except ET.ParseError:
        return None
    node = root.find("./licenses/license/name")
    return node.text.strip() if node is not None and node.text else None


def fetch(name: str) -> PackageFacts:
    group, artifact = _coordinates(name)
    facts = PackageFacts(Ecosystem.MAVEN, name)
    base_url = f"{REPO}/{group.replace('.', '/')}/{artifact}"

    try:
        metadata = get_text(f"{base_url}/maven-metadata.xml")
    except NotFound:
        facts.exists = False
        return facts

    facts.exists = True
    try:
        root = ET.fromstring(metadata)
    except ET.ParseError as err:
        raise SourceUnavailable(f"unparseable maven-metadata.xml for {name}") from err

    versioning = root.find("versioning")
    versions: list[str] = []
    if versioning is not None:
        latest = versioning.findtext("release") or versioning.findtext("latest")
        facts.latest_version = latest
        versions = [
            v.text for v in versioning.findall("./versions/version") if v.text
        ]

    dates = _listing_dates(base_url)
    if not dates:
        facts.degraded.append("maven directory listing (release dates unavailable)")
    releases = [ReleaseRef(v, dates.get(v)) for v in versions]
    releases.sort(key=lambda r: (r.release_date or dt.date.min), reverse=True)
    facts.releases = releases

    if facts.latest_version:
        facts.license = _pom_license(base_url, artifact, facts.latest_version)
    return facts
