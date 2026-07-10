"""Ports of com.depmesh.backend.model.ArtifactReleasePaceMetricsBuilderTest
plus coverage for the state machine. The one-week and two-day fixtures are
the original 2015 test data, unchanged.
"""
import datetime as dt

from depmesh_ai.metrics import (
    PaceState,
    build_release_pace,
    calculate_average_pace,
)
from depmesh_ai.model import ReleaseRef


def ref(version: str, date: str | None) -> ReleaseRef:
    return ReleaseRef(version, dt.date.fromisoformat(date) if date else None)


def test_average_pace_one_week_average():
    # Legacy fixture: expected PT168H (7 days)
    ordered = [
        ref("3", "2015-01-15"),
        ref("2", "2015-01-08"),
        ref("1", "2015-01-01"),
    ]
    assert calculate_average_pace(ordered, 0, len(ordered) - 1) == dt.timedelta(days=7)


def test_average_pace_two_days_average():
    # Legacy fixture: expected PT48H (2 days)
    ordered = [
        ref("6", "2015-01-09"),
        ref("5", "2015-01-07"),
        ref("3", "2015-01-05"),
        ref("2", "2015-01-03"),
        ref("1", "2015-01-01"),
    ]
    assert calculate_average_pace(ordered, 0, len(ordered) - 1) == dt.timedelta(days=2)


def test_average_pace_skips_undated_releases():
    ordered = [
        ref("3", "2015-01-15"),
        ref("2", None),
        ref("1", "2015-01-01"),
    ]
    assert calculate_average_pace(ordered, 0, len(ordered) - 1) == dt.timedelta(days=14)


def test_no_data():
    assert build_release_pace([]).state is PaceState.NO_DATA


def test_not_enough_data():
    metrics = build_release_pace([ref("2", "2020-02-01"), ref("1", "2020-01-01")])
    assert metrics.state is PaceState.NOT_ENOUGH_DATA
    assert metrics.latest_release.version == "2"
    assert metrics.first_release.version == "1"


def test_ok_computes_both_paces():
    metrics = build_release_pace(
        [
            ref("4", "2020-04-01"),
            ref("3", "2020-03-01"),
            ref("2", "2020-02-01"),
            ref("1", "2020-01-01"),
        ]
    )
    assert metrics.state is PaceState.OK
    assert metrics.first_release.version == "1"
    assert metrics.latest_release.version == "4"
    assert metrics.overall_pace == dt.timedelta(days=91) / 3
    assert metrics.last3_pace == dt.timedelta(days=60) / 2
