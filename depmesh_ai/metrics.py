"""Release-pace metrics, ported from the legacy DepMesh backend.

Java original: com.depmesh.backend.model.ArtifactReleasePaceMetrics.
The semantics are preserved (including the newest-at-index-0 list
contract and the NO_DATA / NOT_ENOUGH_DATA / OK states) so the original
unit-test fixtures carry over — see tests/test_metrics.py.
"""
from __future__ import annotations

import datetime as dt
from dataclasses import dataclass
from enum import Enum
from typing import Optional

from .model import ReleaseRef


class PaceState(str, Enum):
    NO_DATA = "NO_DATA"
    NOT_ENOUGH_DATA = "NOT_ENOUGH_DATA"
    OK = "OK"


@dataclass
class ReleasePaceMetrics:
    state: PaceState
    first_release: Optional[ReleaseRef] = None
    latest_release: Optional[ReleaseRef] = None
    overall_pace: Optional[dt.timedelta] = None
    last3_pace: Optional[dt.timedelta] = None


def calculate_average_pace(
    ordered_refs: list[ReleaseRef], start: int, to_inclusive: int
) -> dt.timedelta:
    """Average gap between consecutive releases in ordered_refs[start..to].

    ordered_refs is newest-first. Releases without a date are skipped and
    excluded from the divisor, mirroring the legacy invalidDataPoints
    handling.
    """
    total = dt.timedelta(0)
    invalid = 0
    previous: Optional[dt.date] = None
    for i in range(start, to_inclusive + 1):
        release_date = ordered_refs[i].release_date
        if release_date is not None:
            if previous is not None:
                total += previous - release_date
            previous = release_date
        else:
            invalid += 1
    return total / max(to_inclusive - start - invalid, 1)


def build_release_pace(ordered_refs: list[ReleaseRef]) -> ReleasePaceMetrics:
    if not ordered_refs:
        return ReleasePaceMetrics(PaceState.NO_DATA)
    if len(ordered_refs) < 3:
        return ReleasePaceMetrics(
            PaceState.NOT_ENOUGH_DATA,
            first_release=ordered_refs[-1],
            latest_release=ordered_refs[0],
        )
    return ReleasePaceMetrics(
        PaceState.OK,
        first_release=ordered_refs[-1],
        latest_release=ordered_refs[0],
        overall_pace=calculate_average_pace(ordered_refs, 0, len(ordered_refs) - 1),
        last3_pace=calculate_average_pace(ordered_refs, 0, 2),
    )
