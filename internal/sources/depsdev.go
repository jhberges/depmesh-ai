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
	if facts.Exists == nil || !*facts.Exists || facts.LatestVersion == "" {
		return
	}
	endpoint := fmt.Sprintf(
		"%s/systems/%s/packages/%s/versions/%s",
		depsDevAPI,
		depsDevSystem[facts.Ecosystem],
		url.PathEscape(facts.Name),
		url.PathEscape(facts.LatestVersion),
	)
	var doc depsDevVersion
	if err := getJSON(endpoint, &doc); err != nil {
		facts.Degraded = append(facts.Degraded, "deps.dev (unreachable — advisories unknown)")
		return
	}
	facts.AdvisoryCount = model.Int(len(doc.AdvisoryKeys))
	if facts.License == "" && len(doc.Licenses) > 0 {
		facts.License = doc.Licenses[0]
	}
}
