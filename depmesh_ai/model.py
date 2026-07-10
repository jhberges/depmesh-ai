"""Core data model.

The vocabulary here descends from the original DepMesh backend model
(com.depmesh.backend.model): Artifact/Version/VersionRef become
PackageFacts/ReleaseRef; the metric classes live in metrics.py.
"""
from __future__ import annotations

import datetime as dt
from dataclasses import dataclass, field
from enum import Enum
from typing import Optional


class Ecosystem(str, Enum):
    NPM = "npm"
    PYPI = "pypi"
    MAVEN = "maven"

    @classmethod
    def parse(cls, value: str) -> "Ecosystem":
        try:
            return cls(value.lower())
        except ValueError:
            raise ValueError(
                f"unknown ecosystem {value!r}; expected one of "
                f"{', '.join(e.value for e in cls)}"
            ) from None


@dataclass(frozen=True)
class ReleaseRef:
    """A single released version. Descendant of legacy VersionRef."""

    version: str
    release_date: Optional[dt.date] = None


@dataclass
class PackageFacts:
    """Everything a source layer could learn about one package.

    `exists` is tri-state on purpose: True/False only when the registry
    answered authoritatively. A network failure must never be reported as
    "does not exist" — that would turn an outage into a slopsquatting
    false alarm (or worse, mask one).
    """

    ecosystem: Ecosystem
    name: str
    exists: Optional[bool] = None
    # Ordered newest-first, matching the legacy builder's contract.
    releases: list[ReleaseRef] = field(default_factory=list)
    latest_version: Optional[str] = None
    license: Optional[str] = None
    description: Optional[str] = None
    deprecated: bool = False
    deprecation_message: Optional[str] = None
    maintainer_count: Optional[int] = None
    homepage: Optional[str] = None
    repository_url: Optional[str] = None
    advisory_count: Optional[int] = None
    # Sources that could not be reached; signals depending on them degrade
    # to "unknown" instead of guessing.
    degraded: list[str] = field(default_factory=list)
