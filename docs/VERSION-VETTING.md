# Plan: version-aware vetting

Today a verdict is about a *package*: `vet maven org.apache.commons:commons-lang3`
answers "is this library worth adopting?". Nothing in the pipeline carries a
version — `sources.Gather`, `vet.Vet`, `gate.Vet`, the MCP tool schema and the
`/v1/vet/{ecosystem}/{package...}` route all stop at the name, and
`internal/sources/depsdev.go` pins its lookup to `facts.LatestVersion`.

That is the right answer to the adoption question and the wrong answer to the
question a build tool asks. Every Gradle, npm, or Poetry dependency *has* a
pinned version, and the properties that decide whether it is safe are the ones
that change between versions:

- **advisories** — the whole point of a CVE is that it applies to a range
- **licenses** — relicensing happens (Terraform, Redis, Sentry, Elastic)
- **deprecation / yanks** — npm deprecates single versions; PyPI yanks single
  releases; today we only look at the latest one
- **currency** — the package can be thriving while your pin is four years and
  sixty releases behind

So the engine can report `ADOPT` on a healthy package while the coordinate in
your build file is the exact thing you should be worried about.

## 1. Two questions, one verdict

The fix is *not* to redirect the existing signals at the requested version. A
version-only verdict loses information a package-level verdict has, and the
reverse is also true:

| | package healthy | package unhealthy |
|---|---|---|
| **version current** | adopt | abandoned upstream — the pin can't save you |
| **version stale** | upgrade, don't drop | drop |

Those are four different actions, and a caller can only pick between them if
the verdict says which half the problem is in. So: keep every existing signal
computed exactly as it is now, and *add* a family of version signals alongside
them. `Signal.Name` already carries the distinction for free — the new ones are
prefixed `version-`, they land in the same `Signals` slice, and scoring,
rendering, JSON, and `Outcome.Allowed()` need no structural change.

With no version requested, the output is byte-identical to today's. That keeps
the CLI, MCP, and API contracts intact and lets the existing test corpus stand
unchanged.

## 2. What the sources already give us

Before designing signals, the honest question is what per-version data is
actually reachable — and the answer is better than expected. Most of it is
already in documents we fetch and then throw away.

| | version exists | release date | license | deprecated / yanked | advisories |
|---|---|---|---|---|---|
| **npm** | `dist-tags` + `time` keys | ✅ `time[v]`, RFC3339 | ✅ `versions[v].license` | ✅ `versions[v].deprecated` | via deps.dev |
| **PyPI** | `releases` keys (normalize!) | ✅ `releases[v][].upload_time_iso_8601` | ⚠️ one extra request | ✅ `releases[v][].yanked` | via deps.dev |
| **Maven** | `maven-metadata.xml` list | ⚠️ day granularity, scraped, often absent | ✅ per-version POM | ✗ no concept | via deps.dev |
| **NuGet** | registration pages, ⚠️ *not* a full enumeration | ✅ `published` per entry | ✅ `licenseExpression` per entry | ✅ `deprecation` per entry | via deps.dev |
| **crates.io** | `versions[]`, one request | ✅ `created_at` | ✅ `license` per version | ✅ `yanked` + `yank_message` | via deps.dev |
| **Go** | `@v/list`, ⚠️ tagged versions only | ⚠️ one `.info` request each | ✗ none published | ✅ `retract` + `// Deprecated:` | via deps.dev |
| **Packagist** | p2 list, ⚠️ tagged versions only | ✅ `time` per version | ⚠️ delta-encoded | ✅ `abandoned` | ✗ not covered |
| **pub.dev** | `versions[]`, one request | ✅ `published` | ✗ none per version | ✅ `retracted` | ✗ not covered |
| **Hex** | `releases[]`, one request | ✅ `inserted_at` | ⚠️ one extra request | ✅ `retirements[v]` | ✗ not covered |
| **deps.dev** | — | — | ✅ | — | ✅ **already version-addressed** |

Three of those cells are the plan's cheapest wins:

- **`internal/sources/depsdev.go:39` is a one-line change.** The endpoint is
  already `/v3/systems/{system}/packages/{package}/versions/{version}` — it
  just hardcodes `facts.LatestVersion`. Pass the requested version and
  advisories become version-accurate immediately. This is the signal that
  varies most between versions and it costs one substitution. The reason
  string in `vet.go` ("affect the latest version") becomes wrong at the same
  moment and must change with it.
- **`sources.pomLicense(baseURL, artifact, version)` is already parameterized
  by version** (`maven.go:99`). Passing the target version instead of the
  latest is free — same single request, different coordinate.
- **npm and PyPI already decode every version.** `npmDoc.Versions` is a full
  `map[string]npmVersion` with license and deprecation for each, and
  `pypiDoc.Releases` carries `yanked` per file; we read both only for the
  latest version (`npm.go:81-92`, `pypi.go:77-79`). Per-version facts for
  npm cost **zero** additional HTTP requests, and PyPI needs one only for
  license — which deps.dev also supplies.

Maven release dates are the weak spot, and they were already weak: they come
from a regex over an HTML directory listing, at day granularity, and
`listingDates` returning nothing is recorded in `Degraded`. Everything below
that divides by a date has to degrade to "unknown", not to "fine".

The ecosystems that landed after this plan was written cost more than a
substitution, and each for its own reason:

- **A withdrawn version is not in the release history.** crates.io yanks and
  pub.dev retractions are deliberately kept out of `Releases`, because counting
  them would credit a package for releases nobody can install. But a caller
  pinning a yanked version is precisely the case that most needs an answer, so
  both adapters index every version and read the requested one from there —
  which is also where its date and its yank message come from.
- **Packagist's list is delta-encoded.** After the first entry a field is
  omitted when it has not changed, so reading a version's license off its own
  entry reports "no license" for every release that simply kept the one it had
  — most of them. `packagistExpand` fills the omissions back in, in the order
  Composer itself expands. That reads oddly for `abandoned`, where an older
  release inherits a marker added after it, and it is right: abandonment is a
  property of the package, not of the release you pinned.
- **Go has no per-version license and no yank.** What it has instead is
  `retract` in the *latest* `go.mod` — the module announcing that a release
  should not be used, since the proxy is immutable and nothing can be removed.
  That file is already fetched for the deprecation marker, so retractions cost
  nothing beyond parsing, including the interval form (`[v1.3.0, v1.4.0]`),
  which needs the version comparator the adapter already has. Deprecation
  itself is declared per version, so a pin from before the announcement does
  not carry it — worth one request to answer honestly.
- **Hex spends one request on the release document.** Retirement is in hand for
  every version already; the license is not, and a relicensing between the pin
  and the latest release is one of the things a version answer is for. It fails
  soft: an unreachable release document leaves the license empty, exactly as if
  it had never been asked.

### Two traps in version identity

**PyPI normalizes versions (PEP 440).** `1.0` and `1.0.0`, `2.0rc1` and
`2.0.0-rc1` may be the same release under different spellings, so an exact
string lookup in `doc.Releases` can miss a version that genuinely exists.

**A lookup miss must never be reported as "version does not exist."** This is
the same rule `PackageFacts.Exists` already encodes as a tri-state: a network
failure is not proof of absence, and neither is a spelling we failed to
normalize. `VersionFacts.Exists` is therefore also `*bool` — `false` only when
the registry authoritatively enumerated its versions and this was not among
them.

The stakes are asymmetric on purpose. A false "exists" is a missed warning; a
false "does not exist" tells a developer that a real, working, pinned
dependency is a hallucination. Fail to the former.

Which leaves the question of how a miss ever becomes a denial, since we cannot
tell a normalization failure from a genuine absence by staring harder at the
list. The answer is to stop guessing and ask: on a miss, request that one
version directly from an endpoint that is authoritative about it —
`/pypi/{name}/{version}/json` for PyPI, the `.pom` for a Maven coordinate — and
let `ErrNotFound` be the only thing that sets `Exists` false. That costs one
extra request on the miss path alone, and it is the difference between a
trustworthy REJECT and an accusation. npm needs no such call: its packument
enumerates every published version and npm publishes only canonical semver, so
the enumeration is itself authoritative.

Three of the later ecosystems make that rule load-bearing rather than
defensive, because their lists are *known* to be partial:

- **NuGet** pages its registration index and the budget reads four pages, so
  the entries in hand are deliberately not an enumeration. The registration
  leaf for one version is.
- **Go** lists tagged versions in `@v/list`, and a `go.mod` pinning an untagged
  commit holds a pseudo-version that is never in it and resolves perfectly
  well. `@v/{version}.info` is what denies one.
- **Packagist** keeps branch versions (`dev-main`, `1.x-dev`) in a separate
  `~dev` document, so a `composer.json` can pin something the releases document
  has never heard of. Only after both are read is an absence real.

The remaining adapters confirm against a per-version endpoint too
(`/crates/{crate}/{version}`, `/packages/{p}/versions/{v}` on pub.dev,
`/packages/{p}/releases/{v}` on Hex) even though their one response is a
complete enumeration today. The cost is one request on a path that only runs
when a spelling failed to match; the alternative is betting a REJECT on a
registry never paginating.

## 3. Scope boundary: exact versions only

Build tools hand out constraints, not versions: `[1.0,2.0)`, `1.+`,
`latest.release`, `^4.18.0`, `~=2.31`, `1.0.0-SNAPSHOT`. Resolving those
correctly means a semver implementation per ecosystem, with Maven's ordering
rules and PEP 440 alongside it, inside a binary whose selling point is that it
has no dependencies.

**We don't resolve ranges.** The gate accepts one exact version and rejects a
constraint with an error that says to resolve it first. This costs nothing in
practice: Gradle, npm, and pip have all *already* resolved the graph by the
time a plugin can enumerate it, and the resolved version is the one that
matters anyway.

Detecting "this is a range, not a version" is a small character check
(`^~*+[](),` , a trailing `.+`, `latest.`, whitespace) and must err toward
accepting — an odd-but-exact version like `1.0.0.Final` or `2.0-20240115.RC3`
has to pass through.

`-SNAPSHOT` is a separate answer: it is exact, it exists, and it is
unvettable, because its contents change without the coordinate changing. Say
that as a signal rather than pretending to have vetted it.

## 4. Version signals

Locate the requested version in `facts.Releases` (already sorted newest-first
by date) to get `idx`, then:

| Signal | Delta | Condition |
|---|---|---|
| `version-existence` | **-100 → REJECT** | registry enumerated its versions and this is not one |
| `version-yanked` | -50 | yanked on PyPI / deprecated on npm, *this* version |
| `version-advisories` | -20 × min(n,3) | advisories against this version (deps.dev) |
| `version-license` | -15 / -10 | this version's license differs from the latest, or is absent/copyleft |
| `version-currency` | 0 → -25 | cadence-normalized lag — see §5 |
| `version-prerelease` | -30 | prerelease pinned (`-rc`, `-beta`, `.dev`, `-M1`) |
| `version-snapshot` | -25 | `-SNAPSHOT`: mutable, cannot be vetted |
| `version-latest` | 0 | this *is* the latest release |
| `version-superseded` | 0 | superseded for over a year — context, not a charge |
| `version-cadence-shift` | 0 | the project slowed markedly after this release |

`version-prerelease` is deliberately heavy enough to cost a band on its own:
pinning a release candidate in production deserves a caution even when
everything else about the package is perfect. The two 0-delta signals are
reported rather than scored because the package-level `staleness` signal already
prices a project that has gone quiet, and charging for the same silence twice
would be double counting.

A nonexistent version is a REJECT of the same family as a nonexistent package,
and it is a real agent failure mode — models invent plausible version numbers
(`express@4.18.99`) as readily as plausible names.

It is *not* the same telemetry event, though. Slopsquatting works because an
attacker can register an unclaimed **name**; they cannot publish `4.18.99` of
someone else's package. A hallucinated version is not a registrable target, so
it does not belong in the slopsquat feed. `docs/TELEMETRY-PROTOCOL.md` is
explicit that a new `kind` requires shipping the receiver before the producers
(line 65), so **phase 1 reports no telemetry for nonexistent versions** and the
question of whether a `hallucinated_version` kind is worth a receiver change is
deferred.

### Scoring budget

Version penalties must not be able to turn a healthy package into a REJECT on
**lag alone**. "You are behind" is an upgrade task; "this is unsafe" is a
block. So `version-currency` and `version-prerelease` are capped to move a
verdict at most one band (ADOPT → CAUTION), while `version-existence`,
`version-yanked`, and `version-advisories` are free to reach REJECT on their
own. Enforce it by clamping the currency/prerelease contribution rather than by
hoping the arithmetic works out.

## 5. Extrapolating cadence

This is where the version pays for itself, and the machinery is mostly already
built.

`metrics.CalculateAveragePace(refs, start, toInclusive)` **already takes an
arbitrary window** — the legacy port happens to expose exactly the primitive a
version-anchored cadence needs. `BuildReleasePace` only ever calls it with
`(0, len-1)` and `(0, 2)`.

Given `idx`, the index of the requested version:

| Derived | Computation |
|---|---|
| `lag` | `refs[0].date - refs[idx].date` |
| `releasesSince` | `idx`, after filtering (below) |
| `paceAtVersion` | `CalculateAveragePace(refs, idx, idx+2)` — cadence when this version shipped |
| **`intervalsBehind`** | **`lag / overallPace`** |
| `supersededFor` | `now - refs[idx-1].date` — how long this version has been superseded |
| `cadenceShift` | `last3Pace / paceAtVersion` — did the project accelerate or decay after this version |
| **`overdue`** | **`(now - refs[0].date) / overallPace`** |

`intervalsBehind` is the headline. Raw lag is nearly meaningless on its own:
400 days behind is routine for a library that ships annually and alarming for
one that ships fortnightly. Normalizing by the project's own cadence gives a
number that means the same thing across ecosystems — "you are eleven releases'
worth of time behind" — and it is exactly the kind of judgement a fixed
threshold cannot make.

`overdue` is the same trick applied at package level, and it fixes an existing
weakness: `staleYearsSoft`/`staleYearsHard` (2y/5y, `vet.go:33`) are absolute,
so they call a healthy annual-release library stale and stay silent about a
weekly-release project that has gone quiet for eight months. Cadence-relative
staleness catches abandonment far earlier: a weekly-release project quiet for
200 days is flagged, where the two-year threshold said nothing.

The absolute thresholds stay as a **floor, not merely a fallback** — take the
worse of the two readings. The tempting version of this idea is to let cadence
replace absolute age entirely, so that a project with a three-year average gap
is not called stale two and a half years in. That is wrong from the consumer's
side: a dependency nobody has shipped in three years has three years of
unpatched anything in it, however leisurely its normal rhythm. Cadence-relative
staleness is there to catch decay *sooner*, never to excuse it.

Cadence-relative staleness also needs a floor under the quiet period itself
(90 days). Without one, a project that ships twice a week is "eight intervals
overdue" after a fortnight, and a fortnight of silence is not abandonment
however fast a project normally moves.

`cadenceShift` answers a question no current signal touches: *did this project
decay after we adopted it?* A dependency pinned when releases came every two
weeks, on a project that has since gone silent for a year, is a different risk
from one that was always slow.

### Counting "releases behind" honestly

`releasesSince = idx` is tempting because it needs no version ordering at all —
just the date sort we already have. It also over-counts, in ways worth fixing
before the number is shown to anyone:

- **Parallel maintenance branches.** A project shipping `3.x` and `4.x`
  concurrently interleaves them by date, so a `4.0.0` user appears "behind"
  every `3.x` patch. Count only refs whose version compares *greater* than the
  target once a comparator exists (phase 3); until then, say "releases
  published since" rather than "releases behind" — it is the claim the data
  actually supports.
- **Prereleases.** Filter them out of the count. Anything containing `-`,
  `rc`, `beta`, `alpha`, `dev`, `.M`, or `snapshot` is not a release you are
  behind on.
- **Yanked and deprecated versions.** Not upgrade targets, so not part of the
  gap.
- **Missing dates.** Maven refs frequently have `ReleaseDate == nil`, and
  `sortNewestFirst` parks those at the end of the list. A target version with
  no date has no computable lag: report unknown and add to `Degraded`.

### The backport anchor bug

`BuildReleasePace` uses `refs[0]` as `LatestRelease`, i.e. the newest by *date*
— which for a project doing parallel maintenance is a backport, not
`dist-tags.latest`. That is the third known gap listed in the README's Status
section, and it is currently a package-level bug affecting the `staleness`
signal.

Version-aware lookup is the fix, and it comes free: once we can locate an
arbitrary version by name, we can locate `facts.LatestVersion` too and anchor
staleness there instead of at index 0. Worth doing in the same phase, because
every cadence number above uses `refs[0]` as the far end of the lag.

**One exception, and it matters.** A newer *prerelease* above the declared
latest is the opposite situation from a backport. Adapters that can tell a
prerelease apart (NuGet does) report the newest *stable* version as the latest,
because that is what a reader installs and what advisories are looked up
against — so a `3.0.0-beta1` published last week above a stable `2.0.0` is not a
backport, it is someone actively working. Re-anchoring there would declare a
busy project stale.

The two cases are indistinguishable by date, and separating them properly needs
version ordering, which is P3. What is available now is enough: a **stable**
release above the declared latest is a backport, a **prerelease** above it is
activity. So the anchor moves only when the date-newest release is stable.

### Two guards

`CalculateAveragePace(refs, i, i)` returns `0` — divisor clamps to 1 and the
loop never adds anything. A zero pace reads as "instantaneous releases" and
divides badly. `paceAtVersion` must therefore require a real window
(`idx+2 <= len-1`) and report `NotEnoughData` otherwise.

Every cadence-normalized signal divides by a pace, so it needs
`State == metrics.OK` **and** a non-zero divisor. Guard both, and degrade to a
stated "unknown" rather than to a silent zero penalty — the existing
`release-history` signal (-15 / -10 for no data) is the pattern to follow.

## 6. Plumbing

### `internal/model`

```go
// VersionFacts is what the sources could learn about one requested version.
// Exists is tri-state for the same reason PackageFacts.Exists is: a
// normalization miss or a network failure is not proof of absence.
type VersionFacts struct {
    Version            string
    Exists             *bool
    ReleaseDate        *time.Time
    License            string
    Deprecated         bool
    DeprecationMessage string
    Yanked             bool
    AdvisoryCount      *int
    IsLatest           bool
}
```

`PackageFacts` gains `Requested *VersionFacts` — nil when no version was
requested, which is the switch that keeps today's behavior byte-identical.
Derived cadence numbers live in `internal/metrics`, not here; this struct holds
only what a source observed.

### Signature changes

`sources.Gather(eco, name, version, enrich)` → every fetcher and
`enrichDepsDev`; `vet.Vet(ecosystem, name, version, enrich)`;
`gate.Gate.Vet(caller, ecosystem, name, version, enrich)`. `vet.Verdict` gains
`Version string` alongside `LatestVersion`.

### Surfaces

- **CLI** — `--version` flag as the explicit form, plus ecosystem-idiomatic
  suffixes because that is how people paste coordinates: `g:a:v` (a third
  colon segment), `express@4.18.2`, `requests==2.31.0`. The explicit flag
  wins. **npm scoped names start with `@`** (`@types/node`), so parse from the
  *last* `@` and only when it is not at index 0.
- **API** — `?version=` **query parameter, not a path segment.** The route ends
  in `{package...}`, which is greedy and already swallows both slashes and
  colons; a Maven GAV in the path would be ambiguous with the coordinate
  itself. Query param is unambiguous and backward compatible.
- **MCP** — optional `version` property on `vet_dependency`, and a tool
  description that tells the agent to pass the version it is about to pin.
  This is the highest-leverage surface: it is the one place the version is
  known *before* anything is written to a build file.
- **`upstream.Request`** — gains `Version`, appended to the query alongside the
  existing `enrich=false`.
- **Audit** — a new `"package_version"` key. Note that `Record.Version` is
  already taken: it marshals as `json:"tool_version"` (`audit.go:31`). Reusing
  the Go field name for the package version is a trap that would silently
  overwrite the build version in every record, so name the field
  `PackageVersion`.

### Policy

Small, and version-shaped:

```json
{ "max_releases_behind": 20,
  "max_intervals_behind": 8,
  "allow_prerelease": false,
  "require_latest": false }
```

Exceptions gain an optional `version`. Matching stays backward compatible: an
exception with no version covers every version of that package (today's
meaning), one with a version covers only that version. This is the more useful
form anyway — "we accepted 2.14.1 after review" is the exception people
actually want, and it stops covering the next release automatically.

## 7. Phasing

| Phase | Work | Est. |
|---|---|---|
| **P0** ✅ | `VersionFacts` through the sources; deps.dev one-liner; per-version license / yank / deprecation; tri-state version existence; `version-*` signals | 1–2d |
| **P1** ✅ | Cadence metrics (§5), `LatestVersion` anchor fix, prerelease/yank filtering of the gap count | 1–2d |
| **P2** | CLI / API / MCP / upstream / audit / policy plumbing; README + this doc | 1–2d |
| **P3** | Loose version comparator → true "versions behind", major-version distance, PEP 440 normalization | 1d |

P0 and P1 are self-contained engine work and deliver most of the value on the
existing surfaces. P2 is what the Gradle plugin needs. P3 upgrades the honest
"published since" wording into a real "behind" count.

**P0 and P1 have landed, for all nine ecosystems.** The plan was written when
there were three; the six that followed answer version questions on the same
terms, with what each registry made expensive recorded in §2. Two things came out
differently from the sketch above, both noted in place: absolute staleness is a floor rather than a fallback
(§5), and `version-prerelease` is -30 rather than -15 so that it costs a band on
its own (§4). One thing the sketch left open is settled: a lookup miss is
confirmed against a per-version endpoint before it is ever reported as
non-existence (§2).

Until P2 lands, no surface can pass a version — `gate.Vet` takes one, and the
CLI, MCP server, and API all pass empty. What *is* live on today's surfaces is
the package-level half of P1: staleness is now cadence-relative, and it is
anchored on `dist-tags.latest` rather than on whatever was published most
recently. A delegating gate (`--upstream`) refuses a version outright rather
than forwarding a request the wire format cannot carry, because a package-level
answer to a version-level question is approval for something never examined.

## 8. What this does not do

- **No range resolution** (§3). Callers resolve first.
- **No transitive graph.** One coordinate per call. A whole-graph scan wants a
  batch endpoint; the API has none today, so a resolved Gradle graph would be
  one GET plus registry fan-out per coordinate.
- **No advisory *range* reasoning.** deps.dev is asked about a specific
  version and answers about that version. We do not parse affected-range
  expressions ourselves.
- **No `-SNAPSHOT` verdict.** Flagged as unvettable, not scored as if it were
  a release.
