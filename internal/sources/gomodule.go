package sources

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jhberges/depmesh-ai/internal/model"
)

// Go module source, read from the module proxy rather than from GitHub.
//
// Not api.github.com: unauthenticated it allows 60 requests per hour per IP,
// which one manifest would exhaust, and the 403 that follows has to degrade to
// "unknown" — so the check would stop being useful exactly when it is used in
// anger. The proxy is unmetered and is the path a real build resolves through
// anyway.
//
// Its cost is that dates come one request at a time. `@latest` carries the
// newest version and its date, `@v/list` carries every other version with no
// dates at all, and each date after that is a separate `.info`. A module with
// no dated releases scores -15 on release-history, so leaving them all unread
// would make the whole ecosystem look worse than it is.
var goProxy = "https://proxy.golang.org"

// maxGoVersionInfos bounds the dates bought for one vet. They are spent on the
// newest versions, which decide staleness and recent cadence, and on the
// oldest, which decides the first-publication date behind the young-package
// signal. Middle versions stay in Releases without a date: the release count
// stays honest and the pace metric already skips undated entries.
//
// Ten is chosen for the shape of the signals rather than measured: three
// dated releases is the minimum for a cadence reading, and the young-package
// signal only matters for modules with few enough versions that all of them
// are fetched anyway.
const maxGoVersionInfos = 10

type goVersionInfo struct {
	Version string `json:"Version"`
	Time    string `json:"Time"`
}

func fetchGo(name, version string) (*model.PackageFacts, error) {
	facts := &model.PackageFacts{Ecosystem: model.Go, Name: name}
	base := goProxy + "/" + escapeGoPath(name)

	var latest goVersionInfo
	err := getJSON(base+"/@latest", &latest)
	switch {
	case errors.Is(err, ErrNotFound):
		// @latest can be absent for a module the proxy knows only by tag, so
		// absence is not concluded from it alone — a false "does not exist" is
		// the one answer this tool must never give cheaply.
		versions, listErr := goVersionList(base)
		if listErr != nil {
			return nil, listErr
		}
		if len(versions) == 0 {
			facts.Exists = model.Bool(false)
			return facts, nil
		}
		facts.Exists = model.Bool(true)
		facts.Releases = goReleases(base, versions, goVersionInfo{}, version)
	case err != nil:
		return nil, err
	default:
		facts.Exists = model.Bool(true)
		facts.LatestVersion = latest.Version
		versions, listErr := goVersionList(base)
		if listErr != nil {
			return nil, listErr
		}
		facts.Releases = goReleases(base, versions, latest, version)
	}

	if facts.LatestVersion == "" && len(facts.Releases) > 0 {
		facts.LatestVersion = facts.Releases[0].Version
	}
	// One go.mod, read twice: it carries both the deprecation marker and the
	// retract directives, and retractions are the only per-version withdrawal
	// Go has.
	latestMod := ""
	if facts.LatestVersion != "" {
		latestMod = goModFile(base, facts.LatestVersion)
		facts.Deprecated, facts.DeprecationMessage = goDeprecation(latestMod)
	}
	// The proxy publishes no license and no maintainers; deps.dev covers `go`
	// and fills the license there. Homepage and repository are the module path
	// itself, which the caller already has.

	// Last, because IsLatest depends on LatestVersion being settled.
	if version != "" {
		facts.Requested = goVersionFacts(facts, base, name, version, latestMod)
	}
	return facts, nil
}

// goVersionFacts answers about one requested version.
//
// @v/list is not an enumeration to deny a version from: it lists tagged
// versions the proxy has seen, while a perfectly resolvable pseudo-version
// (v0.0.0-20240115120000-abcdef123456) never appears in it and is what a
// go.mod pinning an untagged commit contains. So a miss goes to `.info` for
// that one version, which is authoritative, and only its 404 denies.
func goVersionFacts(facts *model.PackageFacts, base, name, version, latestMod string) *model.VersionFacts {
	resolved := ""
	for _, candidate := range goSpellings(version) {
		if indexOfRelease(facts.Releases, candidate) >= 0 {
			resolved = candidate
			break
		}
	}

	var confirmed goVersionInfo
	vf := requestedVersion(facts, version, resolved, func() (string, error) {
		for _, candidate := range goSpellings(version) {
			var info goVersionInfo
			err := getJSON(base+"/@v/"+escapeGoPath(candidate)+".info", &info)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return "", err
			}
			confirmed = info
			return firstNonEmpty(info.Version, candidate), nil
		}
		return "", fmt.Errorf("the module proxy has no version %q of %s: %w", version, name, ErrNotFound)
	})
	if vf.Exists == nil || !*vf.Exists {
		return vf
	}
	if vf.ReleaseDate == nil {
		if when, err := time.Parse(time.RFC3339, confirmed.Time); err == nil {
			when := when.UTC()
			vf.ReleaseDate = &when
		}
	}

	// A retraction is the maintainer saying this release should not be used —
	// Go's equivalent of a yank, and unlike one it is announced from the
	// *latest* go.mod rather than by removing anything, so it costs no request
	// beyond the one already made.
	if reason, retracted := goRetraction(latestMod, vf.Resolved); retracted {
		vf.Yanked = true
		vf.DeprecationMessage = firstNonEmpty(reason, "retracted in the module's go.mod")
	}

	// Deprecation is declared in each version's own go.mod, so a pin from
	// before the announcement does not carry it. The latest one is already
	// read; any other version costs one request, which is what makes the
	// difference between "this module is deprecated" and "the release you
	// pinned said so".
	switch {
	case vf.Resolved == facts.LatestVersion:
		vf.Deprecated, vf.DeprecationMessage = facts.Deprecated, firstNonEmpty(facts.DeprecationMessage, vf.DeprecationMessage)
	default:
		if deprecated, message := goDeprecation(goModFile(base, vf.Resolved)); deprecated {
			vf.Deprecated, vf.DeprecationMessage = true, firstNonEmpty(message, vf.DeprecationMessage)
		}
	}
	return vf
}

// goSpellings returns the forms a requested version may be written in. Module
// versions are canonically "v"-prefixed, but lockfiles and humans drop it.
func goSpellings(version string) []string {
	if strings.HasPrefix(version, "v") {
		return []string{version}
	}
	return []string{version, "v" + version}
}

// goVersionList reads @v/list: version strings, one per line, in no
// particular order and with no dates. An empty list is a real answer (a module
// with no tagged releases), so only a transport failure is an error.
func goVersionList(base string) ([]string, error) {
	body, err := getText(base + "/@v/list")
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var versions []string
	for _, line := range strings.Split(body, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			versions = append(versions, line)
		}
	}
	return versions, nil
}

// goReleases orders the versions newest-first and buys dates for as many as
// the budget allows. The ordering is by version rather than by date on
// purpose: it is the ordering @v/list does not provide, it survives the
// versions left undated, and it puts the genuinely oldest release last where
// BuildReleasePace looks for the first publication.
func goReleases(base string, versions []string, latest goVersionInfo, pinned string) []model.ReleaseRef {
	if latest.Version != "" && !slicesContain(versions, latest.Version) {
		versions = append(versions, latest.Version)
	}
	sort.SliceStable(versions, func(i, j int) bool {
		return compareGoVersions(versions[i], versions[j]) > 0
	})

	dates := map[string]time.Time{}
	if when, err := time.Parse(time.RFC3339, latest.Time); err == nil {
		dates[latest.Version] = when.UTC()
	}
	// The pinned version's own date is bought whatever the budget says: it is
	// what "how far behind is this pin" is measured from, and a middle version
	// is exactly where the budget would otherwise leave a hole.
	wanted := selectGoVersions(versions, maxGoVersionInfos)
	if pinned != "" {
		for _, candidate := range goSpellings(pinned) {
			if slicesContain(versions, candidate) && !slicesContain(wanted, candidate) {
				wanted = append(wanted, candidate)
				break
			}
		}
	}
	for _, version := range wanted {
		if _, known := dates[version]; known {
			continue
		}
		var info goVersionInfo
		// A date that will not load costs history, not the verdict.
		if err := getJSON(base+"/@v/"+escapeGoPath(version)+".info", &info); err != nil {
			continue
		}
		if when, err := time.Parse(time.RFC3339, info.Time); err == nil {
			dates[version] = when.UTC()
		}
	}

	releases := make([]model.ReleaseRef, 0, len(versions))
	for _, version := range versions {
		ref := model.ReleaseRef{Version: version}
		if when, ok := dates[version]; ok {
			when := when
			ref.ReleaseDate = &when
		}
		releases = append(releases, ref)
	}
	return releases
}

// selectGoVersions picks which of the newest-first versions to buy dates for:
// the newest n-1, plus the oldest so the first publication is dated.
func selectGoVersions(versions []string, n int) []string {
	if n < 1 || len(versions) == 0 {
		return nil
	}
	if len(versions) <= n {
		return versions
	}
	return append(versions[:n-1:n-1], versions[len(versions)-1])
}

// goModFile fetches one version's go.mod, or "" if it will not load — the
// signals read out of it are all "absent" rather than "false" in that case.
// It is worth the request: deprecation is the heaviest single signal, and the
// proxy offers no other way to see it.
func goModFile(base, version string) string {
	body, err := getText(base + "/@v/" + escapeGoPath(version) + ".mod")
	if err != nil {
		return ""
	}
	return body
}

// goDeprecation reads the module's go.mod. A module is deprecated by a
// "// Deprecated: ..." comment on the block immediately above the module
// directive — the same marker `go list -m -u` reports, and how
// github.com/golang/protobuf announces that it has been replaced.
func goDeprecation(body string) (bool, string) {
	var message []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "module "):
			// The comment block only counts directly above the directive.
			return len(message) > 0, strings.Join(message, " ")
		case strings.HasPrefix(line, "// Deprecated:"):
			message = append(message, strings.TrimSpace(strings.TrimPrefix(line, "// Deprecated:")))
		case line == "" || !strings.HasPrefix(line, "//"):
			message = nil
		case len(message) > 0:
			message = append(message, strings.TrimSpace(strings.TrimPrefix(line, "//")))
		}
	}
	return false, ""
}

// goRetraction reports whether the module's go.mod retracts a version, and the
// reason its comment gives. A retraction is Go's yank: the version stays
// downloadable — the proxy is immutable, so it must — and is instead announced
// as unusable from the *latest* go.mod, which is the one already fetched.
//
// The directive comes in a single form and a block, and either line may name
// one version or a closed interval:
//
//	retract v1.0.1 // published by mistake
//	retract (
//	    [v1.3.0, v1.4.0] // broken on Windows
//	)
//
// Intervals are why this needs compareGoVersions rather than string equality:
// the retracted versions in a range are usually not written down anywhere.
func goRetraction(body, version string) (string, bool) {
	inBlock := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "retract ("):
			inBlock = true
			continue
		case inBlock && line == ")":
			inBlock = false
			continue
		case strings.HasPrefix(line, "retract "):
			line = strings.TrimSpace(strings.TrimPrefix(line, "retract"))
		case !inBlock:
			continue
		}
		spec, comment, _ := strings.Cut(line, "//")
		if retractionCovers(strings.TrimSpace(spec), version) {
			return strings.TrimSpace(comment), true
		}
	}
	return "", false
}

// retractionCovers matches one retract spec: a bare version, or "[low, high]"
// naming a closed interval.
func retractionCovers(spec, version string) bool {
	if spec == "" {
		return false
	}
	if !strings.HasPrefix(spec, "[") {
		// Compared rather than matched, so that a spelling difference the
		// proxy tolerates — a missing "v", "+incompatible" build metadata —
		// does not hide a retraction.
		return compareGoVersions(spec, version) == 0
	}
	low, high, ok := strings.Cut(strings.Trim(spec, "[]"), ",")
	if !ok {
		return false
	}
	low, high = strings.TrimSpace(low), strings.TrimSpace(high)
	return compareGoVersions(version, low) >= 0 && compareGoVersions(version, high) <= 0
}

// escapeGoPath applies the module proxy's case encoding: an uppercase letter
// becomes "!" plus its lowercase form, so case-insensitive filesystems cannot
// collide two different modules. Without it github.com/BurntSushi/toml answers
// 404 — a real, widely used module reported as a hallucinated name.
func escapeGoPath(path string) string {
	var out strings.Builder
	for _, r := range path {
		if r >= 'A' && r <= 'Z' {
			out.WriteByte('!')
			out.WriteRune(r + ('a' - 'A'))
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func slicesContain(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// compareGoVersions orders two module versions by SemVer precedence,
// returning -1, 0 or 1. @v/list arrives unordered, and lexical order is wrong
// in the way that matters (v0.10.0 would sort below v0.9.0), so the tool needs
// its own comparison — golang.org/x/mod/semver would be an external
// dependency, and this repository has none by design.
func compareGoVersions(a, b string) int {
	aCore, aPre := splitGoVersion(a)
	bCore, bPre := splitGoVersion(b)
	for i := range aCore {
		if aCore[i] != bCore[i] {
			if aCore[i] < bCore[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case aPre == bPre:
		return 0
	case aPre == "": // a release outranks any pre-release of the same version
		return 1
	case bPre == "":
		return -1
	}
	return comparePrerelease(aPre, bPre)
}

// splitGoVersion returns the major/minor/patch numbers and the pre-release
// label. Build metadata is dropped, which is what makes "+incompatible"
// compare equal to the plain version, as SemVer requires.
func splitGoVersion(version string) ([3]int, string) {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if plus := strings.IndexByte(version, '+'); plus >= 0 {
		version = version[:plus]
	}
	core, pre, _ := strings.Cut(version, "-")

	var numbers [3]int
	for i, part := range strings.SplitN(core, ".", 3) {
		if i > 2 {
			break
		}
		numbers[i], _ = strconv.Atoi(part)
	}
	return numbers, pre
}

// comparePrerelease applies SemVer's rules for pre-release identifiers:
// numeric ones compare numerically, numeric outranks nothing and is outranked
// by alphanumeric, and a longer list of otherwise equal identifiers wins.
func comparePrerelease(a, b string) int {
	aParts, bParts := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		if aParts[i] == bParts[i] {
			continue
		}
		aNumber, aNotNumeric := strconv.Atoi(aParts[i])
		bNumber, bNotNumeric := strconv.Atoi(bParts[i])
		switch {
		case aNotNumeric == nil && bNotNumeric == nil:
			if aNumber < bNumber {
				return -1
			}
			return 1
		case aNotNumeric == nil: // numeric identifiers rank below alphanumeric
			return -1
		case bNotNumeric == nil:
			return 1
		case aParts[i] < bParts[i]:
			return -1
		default:
			return 1
		}
	}
	switch {
	case len(aParts) == len(bParts):
		return 0
	case len(aParts) < len(bParts):
		return -1
	default:
		return 1
	}
}
