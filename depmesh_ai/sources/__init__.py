"""Source layer: one registry-direct module per ecosystem, plus optional
enrichment (deps.dev). Registry modules are authoritative for existence;
enrichment only adds signals and degrades gracefully when unreachable.
"""
from __future__ import annotations

from ..model import Ecosystem, PackageFacts
from . import depsdev, maven, npm, pypi

_FETCHERS = {
    Ecosystem.NPM: npm.fetch,
    Ecosystem.PYPI: pypi.fetch,
    Ecosystem.MAVEN: maven.fetch,
}


def gather(ecosystem: Ecosystem, name: str, enrich: bool = True) -> PackageFacts:
    facts = _FETCHERS[ecosystem](name)
    if enrich:
        depsdev.enrich(facts)
    return facts
