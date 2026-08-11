package model

import "strings"

// SplitCoordinate separates a pasted coordinate into a package name and a
// version, in whatever spelling that ecosystem's own tooling uses. It returns
// the coordinate unchanged, with an empty version, when there is no version in
// it — which is the common case and must stay one.
//
// This exists because people paste coordinates, they do not retype them as
// flags. The string in front of someone asking whether a dependency is safe is
// the one their build tool prints: `org.example:widget:1.2.0` out of a Gradle
// error, `express@4.18.2` out of an npm install line.
//
// Nothing here validates the version. A range or a wildcard survives this
// function intact and is refused further in, by sources.Gather, with an error
// that says to resolve it first — one place to say that, rather than a second
// opinion here that could disagree with it.
func SplitCoordinate(ecosystem Ecosystem, coordinate string) (name, version string) {
	coordinate = strings.TrimSpace(coordinate)
	switch ecosystem {
	case Maven:
		// groupId:artifactId:version, as Gradle and Maven print it. Exactly
		// three segments: longer forms exist (`g:a:packaging:classifier:v`)
		// and reading the third segment of one as a version would be wrong.
		if parts := strings.Split(coordinate, ":"); len(parts) == 3 {
			return join(parts[0], parts[1]), parts[2]
		}
		return coordinate, ""

	case Packagist, Pub:
		// `composer require monolog/monolog:2.0.0`, `dart pub add http:1.2.0`.
		// Neither ecosystem allows a colon in a package name, so the first one
		// separates.
		if before, after, found := strings.Cut(coordinate, ":"); found && after != "" {
			return before, after
		}
		return coordinate, ""

	case PyPI:
		// `pip install requests==2.31.0`. A single "=" is not a pin, and the
		// other comparison operators (>=, ~=) are ranges — left intact so that
		// they are refused as ranges rather than silently read as a version.
		if before, after, found := strings.Cut(coordinate, "=="); found && after != "" {
			return before, after
		}
		return splitAtVersionMarker(coordinate)

	default:
		// `express@4.18.2`, `cargo add serde@1.0.197`, `go get
		// example.com/mod@v1.2.3`.
		return splitAtVersionMarker(coordinate)
	}
}

// splitAtVersionMarker reads the `name@version` form.
//
// The "@" is taken from the right and never at index 0, because npm scoped
// names start with one: `@types/node` is a name, `@types/node@20.1.0` is a name
// and a version.
func splitAtVersionMarker(coordinate string) (name, version string) {
	at := strings.LastIndexByte(coordinate, '@')
	if at <= 0 || at == len(coordinate)-1 {
		return coordinate, ""
	}
	return coordinate[:at], coordinate[at+1:]
}

func join(group, artifact string) string { return group + ":" + artifact }
