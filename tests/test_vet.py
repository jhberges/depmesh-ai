import datetime as dt

from depmesh_ai.model import Ecosystem, PackageFacts, ReleaseRef
from depmesh_ai.vet import Advice, evaluate

TODAY = dt.date(2026, 7, 10)


def healthy_facts() -> PackageFacts:
    return PackageFacts(
        ecosystem=Ecosystem.NPM,
        name="good-lib",
        exists=True,
        releases=[
            ReleaseRef("3.0.0", TODAY - dt.timedelta(days=30)),
            ReleaseRef("2.0.0", TODAY - dt.timedelta(days=200)),
            ReleaseRef("1.0.0", TODAY - dt.timedelta(days=400)),
        ],
        latest_version="3.0.0",
        license="MIT",
        maintainer_count=4,
        advisory_count=0,
    )


def test_nonexistent_package_is_rejected():
    facts = PackageFacts(Ecosystem.NPM, "hallucinated-lib", exists=False)
    verdict = evaluate(facts, today=TODAY)
    assert verdict.advice is Advice.REJECT
    assert verdict.score == 0
    assert "does not exist" in verdict.signals[0].reason


def test_healthy_package_is_adopted():
    verdict = evaluate(healthy_facts(), today=TODAY)
    assert verdict.advice is Advice.ADOPT
    assert verdict.score >= 75


def test_deprecated_package_is_penalized():
    facts = healthy_facts()
    facts.deprecated = True
    facts.deprecation_message = "use other-lib instead"
    verdict = evaluate(facts, today=TODAY)
    assert verdict.advice in (Advice.CAUTION, Advice.REJECT)
    assert any(s.name == "deprecation" for s in verdict.signals)


def test_stale_package_is_penalized():
    facts = healthy_facts()
    facts.releases = [
        ReleaseRef("1.2.0", TODAY - dt.timedelta(days=6 * 365)),
        ReleaseRef("1.1.0", TODAY - dt.timedelta(days=7 * 365)),
        ReleaseRef("1.0.0", TODAY - dt.timedelta(days=8 * 365)),
    ]
    verdict = evaluate(facts, today=TODAY)
    assert verdict.advice is not Advice.ADOPT
    assert any(s.name == "staleness" and s.delta < 0 for s in verdict.signals)


def test_brand_new_package_flags_slopsquatting_risk():
    facts = healthy_facts()
    facts.releases = [
        ReleaseRef("1.0.1", TODAY - dt.timedelta(days=3)),
        ReleaseRef("1.0.0", TODAY - dt.timedelta(days=5)),
        ReleaseRef("0.0.1", TODAY - dt.timedelta(days=6)),
    ]
    verdict = evaluate(facts, today=TODAY)
    assert any(s.name == "package-age" for s in verdict.signals)
    assert verdict.advice is not Advice.ADOPT


def test_missing_license_is_penalized():
    facts = healthy_facts()
    facts.license = None
    verdict = evaluate(facts, today=TODAY)
    assert any(s.name == "license" and s.delta < 0 for s in verdict.signals)


def test_advisories_are_penalized():
    facts = healthy_facts()
    facts.advisory_count = 2
    verdict = evaluate(facts, today=TODAY)
    assert verdict.advice is not Advice.ADOPT


def test_network_failure_does_not_mean_nonexistent():
    facts = PackageFacts(Ecosystem.PYPI, "unknown-status-lib", exists=None)
    facts.degraded.append("pypi (SourceUnavailable)")
    verdict = evaluate(facts, today=TODAY)
    # exists=None must not trigger the anti-slopsquatting REJECT path
    assert verdict.score > 0
