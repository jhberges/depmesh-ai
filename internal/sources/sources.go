package sources

import (
	"github.com/jhberges/depmesh-ai/internal/model"
)

// Gather fetches authoritative facts from the package's own registry, then
// optionally enriches them from deps.dev.
func Gather(ecosystem model.Ecosystem, name string, enrich bool) (*model.PackageFacts, error) {
	var facts *model.PackageFacts
	var err error
	switch ecosystem {
	case model.NPM:
		facts, err = fetchNPM(name)
	case model.PyPI:
		facts, err = fetchPyPI(name)
	case model.Maven:
		facts, err = fetchMaven(name)
	case model.NuGet:
		facts, err = fetchNuGet(name)
	case model.Cargo:
		facts, err = fetchCargo(name)
	case model.Go:
		facts, err = fetchGo(name)
	case model.Packagist:
		facts, err = fetchPackagist(name)
	case model.Pub:
		facts, err = fetchPub(name)
	case model.Hex:
		facts, err = fetchHex(name)
	}
	if err != nil {
		return nil, err
	}
	if enrich {
		enrichDepsDev(facts)
	}
	return facts, nil
}
