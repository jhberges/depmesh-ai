package sources

import (
	"fmt"
	"net/url"

	"github.com/jhberges/depmesh-ai/internal/model"
)

// Optional enrichment from deps.dev (Google Open Source Insights), which
// aggregates advisories, licenses, and OpenSSF Scorecard signals for 50M+
// package versions. It is the preferred advisory source, but many corporate
// egress policies block it — enrichment therefore degrades gracefully and
// only ever *adds* to registry facts, never replaces them.
const depsDevAPI = "https://api.deps.dev/v3"

var depsDevSystem = map[model.Ecosystem]string{
	model.NPM:   "npm",
	model.PyPI:  "pypi",
	model.Maven: "maven",
	model.NuGet: "nuget",
	model.Cargo: "cargo",
	model.Go:    "go",
}

type depsDevVersion struct {
	Licenses     []string `json:"licenses"`
	AdvisoryKeys []struct {
		ID string `json:"id"`
	} `json:"advisoryKeys"`
}

func enrichDepsDev(facts *model.PackageFacts) {
	if facts.Exists == nil || !*facts.Exists {
		return
	}
	// The endpoint has always been version-addressed; it was simply pinned to
	// the latest release. Advisories are the signal that varies most between
	// versions — a CVE applies to a range, which is the whole point of one — so
	// when a caller named a version, that is the version to ask about.
	target, requested := facts.LatestVersion, facts.Requested
	if requested != nil && requested.Resolved != "" {
		target = requested.Resolved
	}
	if target == "" {
		return
	}
	// Not every ecosystem is covered — packagist and pub are not, and hex is
	// not. That is an absent signal, not a degraded one: reporting deps.dev as
	// unreachable would send the reader looking for an egress problem that
	// does not exist.
	system, covered := depsDevSystem[facts.Ecosystem]
	if !covered {
		return
	}
	endpoint := fmt.Sprintf(
		"%s/systems/%s/packages/%s/versions/%s",
		depsDevAPI,
		system,
		url.PathEscape(facts.Name),
		url.PathEscape(target),
	)
	var doc depsDevVersion
	if err := getJSON(endpoint, &doc); err != nil {
		facts.Degraded = append(facts.Degraded, "deps.dev (unreachable — advisories unknown)")
		return
	}
	// One request, attributed to whichever version it actually described.
	// Asking about both would double the egress for a signal the requested
	// version already answers.
	if target != facts.LatestVersion && requested != nil {
		requested.AdvisoryCount = model.Int(len(doc.AdvisoryKeys))
		if requested.License == "" && len(doc.Licenses) > 0 {
			requested.License = doc.Licenses[0]
		}
		return
	}
	facts.AdvisoryCount = model.Int(len(doc.AdvisoryKeys))
	if facts.License == "" && len(doc.Licenses) > 0 {
		facts.License = doc.Licenses[0]
	}
	if requested != nil && requested.IsLatest {
		requested.AdvisoryCount = facts.AdvisoryCount
		if requested.License == "" {
			requested.License = facts.License
		}
	}
}
