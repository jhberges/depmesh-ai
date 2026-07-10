"""Verdict engine: should this dependency be adopted?

Scores a package 0-100 from the gathered facts and ported DepMesh
metrics, then maps the score to ADOPT / CAUTION / REJECT. Every signal
carries a human-readable reason so the caller (a developer or a coding
agent) can see *why*, not just a number.

Non-existence is special-cased: a package the registry has never heard
of is an immediate REJECT regardless of anything else — that is the
anti-slopsquatting gate.
"""
from __future__ import annotations

import datetime as dt
import json
from dataclasses import dataclass, field
from enum import Enum
from typing import Optional

from .metrics import PaceState, ReleasePaceMetrics, build_release_pace
from .model import Ecosystem, PackageFacts
from . import sources

ADOPT_THRESHOLD = 75
CAUTION_THRESHOLD = 40

# Packages younger than this are prime slopsquatting real estate:
# attackers register names LLMs hallucinate, so a name that appeared on
# the registry last week deserves heavy suspicion.
YOUNG_PACKAGE_DAYS = 60

STALE_YEARS_SOFT = 2
STALE_YEARS_HARD = 5

COPYLEFT_MARKERS = ("GPL", "AGPL", "EUPL", "SSPL")


class Advice(str, Enum):
    ADOPT = "ADOPT"
    CAUTION = "CAUTION"
    REJECT = "REJECT"


@dataclass
class Signal:
    name: str
    delta: int
    reason: str


@dataclass
class Verdict:
    ecosystem: str
    package: str
    advice: Advice
    score: int
    signals: list[Signal]
    pace: Optional[ReleasePaceMetrics] = None
    facts: Optional[PackageFacts] = None
    degraded: list[str] = field(default_factory=list)

    def to_dict(self) -> dict:
        return {
            "ecosystem": self.ecosystem,
            "package": self.package,
            "advice": self.advice.value,
            "score": self.score,
            "signals": [
                {"name": s.name, "delta": s.delta, "reason": s.reason}
                for s in self.signals
            ],
            "latest_version": self.facts.latest_version if self.facts else None,
            "license": self.facts.license if self.facts else None,
            "release_count": len(self.facts.releases) if self.facts else 0,
            "degraded_sources": self.degraded,
        }

    def to_json(self) -> str:
        return json.dumps(self.to_dict(), indent=2)


def _pace_signals(pace: ReleasePaceMetrics, today: dt.date) -> list[Signal]:
    signals: list[Signal] = []
    if pace.state is PaceState.NO_DATA:
        signals.append(
            Signal("release-history", -15, "no dated releases found; cannot judge cadence")
        )
        return signals
    if pace.state is PaceState.NOT_ENOUGH_DATA:
        signals.append(
            Signal(
                "release-history",
                -10,
                f"only {1 if pace.first_release is pace.latest_release else 2} "
                "release(s); too little history to judge cadence",
            )
        )

    latest = pace.latest_release
    if latest and latest.release_date:
        age = today - latest.release_date
        if age > dt.timedelta(days=365 * STALE_YEARS_HARD):
            signals.append(
                Signal(
                    "staleness",
                    -40,
                    f"latest release {latest.version} is {age.days // 365} years old "
                    "— likely unmaintained",
                )
            )
        elif age > dt.timedelta(days=365 * STALE_YEARS_SOFT):
            signals.append(
                Signal(
                    "staleness",
                    -20,
                    f"latest release {latest.version} is over "
                    f"{STALE_YEARS_SOFT} years old",
                )
            )
        else:
            signals.append(
                Signal(
                    "staleness",
                    0,
                    f"latest release {latest.version} is {age.days} days old",
                )
            )
        if age < dt.timedelta(days=YOUNG_PACKAGE_DAYS) and pace.first_release:
            first = pace.first_release.release_date
            if first and (today - first) < dt.timedelta(days=YOUNG_PACKAGE_DAYS):
                signals.append(
                    Signal(
                        "package-age",
                        -35,
                        f"package first published only {(today - first).days} days ago "
                        "— young names are common slopsquatting targets",
                    )
                )

    if pace.state is PaceState.OK and pace.overall_pace is not None:
        signals.append(
            Signal(
                "release-pace",
                0,
                f"average gap between releases: {pace.overall_pace.days} days "
                f"(last three: {pace.last3_pace.days} days)",
            )
        )
    return signals


def evaluate(facts: PackageFacts, today: Optional[dt.date] = None) -> Verdict:
    today = today or dt.date.today()
    signals: list[Signal] = []

    if facts.exists is False:
        return Verdict(
            ecosystem=facts.ecosystem.value,
            package=facts.name,
            advice=Advice.REJECT,
            score=0,
            signals=[
                Signal(
                    "existence",
                    -100,
                    "package does not exist on the registry — if an AI assistant "
                    "suggested it, this is likely a hallucinated (slopsquattable) name",
                )
            ],
            facts=facts,
            degraded=list(facts.degraded),
        )

    signals.append(Signal("existence", 0, "package exists on the registry"))

    if facts.deprecated:
        signals.append(
            Signal(
                "deprecation",
                -50,
                f"deprecated by its maintainers: {facts.deprecation_message or 'no message'}",
            )
        )

    pace = build_release_pace(facts.releases)
    signals.extend(_pace_signals(pace, today))

    if facts.maintainer_count is not None:
        if facts.maintainer_count <= 1:
            signals.append(
                Signal(
                    "maintainers",
                    -10,
                    "single maintainer — bus-factor risk",
                )
            )
        else:
            signals.append(
                Signal(
                    "maintainers", 0, f"{facts.maintainer_count} maintainers"
                )
            )

    if facts.license:
        upper = facts.license.upper()
        if any(marker in upper for marker in COPYLEFT_MARKERS) and "LGPL" not in upper:
            signals.append(
                Signal(
                    "license",
                    -10,
                    f"copyleft license ({facts.license}) — verify compatibility "
                    "with your distribution model",
                )
            )
        else:
            signals.append(Signal("license", 0, f"license: {facts.license}"))
    else:
        signals.append(
            Signal("license", -15, "no license declared — legal risk for adoption")
        )

    if facts.advisory_count is not None:
        if facts.advisory_count > 0:
            signals.append(
                Signal(
                    "advisories",
                    -20 * min(facts.advisory_count, 3),
                    f"{facts.advisory_count} known advisory(ies) affect the latest version",
                )
            )
        else:
            signals.append(
                Signal("advisories", 0, "no known advisories for the latest version")
            )

    score = max(0, min(100, 100 + sum(s.delta for s in signals)))
    if score >= ADOPT_THRESHOLD:
        advice = Advice.ADOPT
    elif score >= CAUTION_THRESHOLD:
        advice = Advice.CAUTION
    else:
        advice = Advice.REJECT

    return Verdict(
        ecosystem=facts.ecosystem.value,
        package=facts.name,
        advice=advice,
        score=score,
        signals=signals,
        pace=pace,
        facts=facts,
        degraded=list(facts.degraded),
    )


def vet(ecosystem: str | Ecosystem, name: str, enrich: bool = True) -> Verdict:
    eco = ecosystem if isinstance(ecosystem, Ecosystem) else Ecosystem.parse(ecosystem)
    return evaluate(sources.gather(eco, name, enrich=enrich))
