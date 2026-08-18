// depmesh-ai: vet open-source dependencies before adopting them.
//
//	depmesh-ai vet npm express
//	depmesh-ai vet maven org.apache.commons:commons-lang3 --json
//	depmesh-ai serve                  # MCP server on stdio
//	depmesh-ai api --listen :8385    # HTTP API, self-hosted inside your network
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jhberges/depmesh-ai/internal/api"
	"github.com/jhberges/depmesh-ai/internal/audit"
	"github.com/jhberges/depmesh-ai/internal/bytesize"
	"github.com/jhberges/depmesh-ai/internal/gate"
	"github.com/jhberges/depmesh-ai/internal/mcp"
	"github.com/jhberges/depmesh-ai/internal/model"
	"github.com/jhberges/depmesh-ai/internal/sources"
	"github.com/jhberges/depmesh-ai/internal/vet"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  depmesh-ai vet [--json] [--no-enrich] [--version V] [--policy FILE] [--audit-log FILE] [--upstream URL] <ecosystem> <package>
  depmesh-ai serve [--policy FILE] [--audit-log FILE] [--upstream URL]
  depmesh-ai api [--listen ADDR] [--policy FILE] [--audit-log FILE]

audit rotation (any surface): --audit-max-size 100MB, --audit-keep 5.
0 means never rotate; both default from the policy file when set there.

ecosystems: `+strings.Join(model.EcosystemStrings(), " | ")+`
            (maven packages: groupId:artifactId)

a version is optional. --version V is the explicit form; a version pasted
onto the coordinate is read the way that ecosystem's own tooling writes it:
  maven      org.springframework.boot:spring-boot-starter-parent:3.2.2
  npm        express@4.18.2, @types/node@20.1.0
  pypi       requests==2.31.0
  go/cargo/nuget/hex      example.com/mod@v1.2.3, serde@1.0.197
  packagist/pub           monolog/monolog:2.0.0, http:1.2.0
Ranges are refused, not resolved: pass the version your build tool resolved.
("depmesh-ai version", with no "vet", prints this tool's own version.)

policy is auto-discovered from ./depmesh.policy.json or $DEPMESH_POLICY;
--policy makes it explicit and errors when the file is missing.

the api listens on :8385 unless --listen or $DEPMESH_LISTEN says otherwise.

--upstream (or $DEPMESH_UPSTREAM) delegates the decision to a central
"depmesh-ai api" instance, so this machine needs no registry access and the
gate's policy and audit log apply. It never falls back to direct registry
access when that gate is unreachable.`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "vet":
		os.Exit(runVet(os.Args[2:]))
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "api":
		os.Exit(runAPI(os.Args[2:]))
	case "version", "--version":
		fmt.Println("depmesh-ai", gate.Version)
	default:
		usage()
	}
}

// gateOptions are the flags every surface shares. The audit rotation flags
// are strings and a sentinel rather than plain values so that "not given"
// stays distinguishable from an explicit 0, which means "never rotate".
type gateOptions struct {
	policyPath *string
	auditLog   *string
	auditMax   *string
	auditKeep  *int
}

const auditKeepUnset = -1

func gateFlags(flags *flag.FlagSet) gateOptions {
	return gateOptions{
		policyPath: flags.String("policy", "", "policy file (default: $DEPMESH_POLICY or ./depmesh.policy.json)"),
		auditLog:   flags.String("audit-log", "", "append JSONL decision records to this file (overrides policy)"),
		auditMax: flags.String("audit-max-size", "",
			"rotate the audit log past this size, e.g. 100MB (0 = never rotate)"),
		auditKeep: flags.Int("audit-keep", auditKeepUnset, "how many rotated audit files to retain"),
	}
}

func upstreamFlag(flags *flag.FlagSet) *string {
	// Backquotes name the placeholder in flag's usage output — hence `URL`.
	return flags.String("upstream", "",
		"delegate to a central depmesh-ai api instance at this `URL` instead of contacting registries (default: $DEPMESH_UPSTREAM)")
}

// resolveUpstream prefers the flag, then the environment. Only the two
// developer-facing surfaces call it: `api` is the far end of the delegation
// and must never chain onward.
func resolveUpstream(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return os.Getenv("DEPMESH_UPSTREAM")
}

// defaultListen is the port the API binds when nothing says otherwise.
const defaultListen = ":8385"

// resolveListen prefers the flag, then the environment, then the default. The
// --listen flag therefore defaults to the empty string rather than to
// defaultListen: with a non-empty flag default, "not given" and "given the
// default" are the same value, and the environment could never win. `--help`
// still shows :8385 because the usage string says so.
//
// The environment matters here because the API is the surface that gets
// deployed rather than invoked, and a deployment configures ports with
// environment variables — the same reason $DEPMESH_POLICY exists.
func resolveListen(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if fromEnv := os.Getenv("DEPMESH_LISTEN"); fromEnv != "" {
		return fromEnv
	}
	return defaultListen
}

func buildGate(options gateOptions, upstreamURL string) *gate.Gate {
	g, err := gate.New(*options.policyPath, *options.auditLog, upstreamURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	if *options.auditMax != "" {
		size, err := bytesize.Parse(*options.auditMax)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: --audit-max-size:", err)
			os.Exit(2)
		}
		g.Audit.MaxSize = size
	}
	if *options.auditKeep != auditKeepUnset {
		if *options.auditKeep < 0 {
			fmt.Fprintln(os.Stderr, "error: --audit-keep cannot be negative")
			os.Exit(2)
		}
		g.Audit.Keep = *options.auditKeep
	}
	// Delegating means the far side decides and records. Saying so beats
	// letting someone believe a local policy file is being enforced.
	if g.Upstream != "" {
		if g.Policy != nil {
			fmt.Fprintln(os.Stderr, "note: --upstream is set; the local policy file is ignored, the gate's policy applies")
		}
		if g.Audit.Path != "" {
			fmt.Fprintln(os.Stderr, "note: --upstream is set; decisions are audited by the gate, not locally")
		}
	}
	return g
}

// coordinate reads a version off the package argument, in the spelling the
// ecosystem's own tooling uses. The explicit flag wins where both are given,
// but the suffix is still stripped from the name — otherwise the flag would
// silently ask about a package called "express@4.18.2".
//
// An unparseable ecosystem is left entirely alone: the gate reports that, with
// the list of the ones that exist, and a split guessed from a typo would only
// change which error comes back.
func coordinate(ecosystem, pkg, flagVersion string) (name, version string) {
	eco, err := model.ParseEcosystem(ecosystem)
	if err != nil {
		return pkg, flagVersion
	}
	name, suffix := model.SplitCoordinate(eco, pkg)
	if flagVersion != "" {
		return name, flagVersion
	}
	return name, suffix
}

func runVet(args []string) int {
	flags := flag.NewFlagSet("vet", flag.ExitOnError)
	asJSON := flags.Bool("json", false, "machine-readable output")
	noEnrich := flags.Bool("no-enrich", false, "skip deps.dev enrichment (advisories)")
	pinned := flags.String("version", "", "vet this exact `version` as well as the package")
	options := gateFlags(flags)
	upstreamURL := upstreamFlag(flags)
	_ = flags.Parse(args)
	if flags.NArg() != 2 {
		usage()
	}
	g := buildGate(options, resolveUpstream(*upstreamURL))

	name, version := coordinate(flags.Arg(0), flags.Arg(1), *pinned)
	outcome, err := g.Vet(audit.Local("cli"), flags.Arg(0), name, version, !*noEnrich)
	var unavailable *sources.UnavailableError
	switch {
	case errors.As(err, &unavailable):
		fmt.Fprintln(os.Stderr, "error: registry unreachable, cannot vet:", err)
		return 2
	case err != nil && outcome == nil:
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	case err != nil:
		// The verdict exists but couldn't be audited: fail closed.
		fmt.Fprintln(os.Stderr, "error: audit log write failed:", err)
		return 2
	}

	if *asJSON {
		fmt.Println(outcome.JSON())
	} else {
		fmt.Print(render(outcome))
	}
	if outcome.Allowed() {
		return 0
	}
	return 1
}

func runServe(args []string) int {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	options := gateFlags(flags)
	upstreamURL := upstreamFlag(flags)
	_ = flags.Parse(args)
	g := buildGate(options, resolveUpstream(*upstreamURL))
	if err := mcp.Serve(g, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	return 0
}

func runAPI(args []string) int {
	flags := flag.NewFlagSet("api", flag.ExitOnError)
	listen := flags.String("listen", "", "listen address (default: $DEPMESH_LISTEN or "+defaultListen+")")
	options := gateFlags(flags)
	_ = flags.Parse(args)
	addr := resolveListen(*listen)
	g := buildGate(options, "")
	fmt.Fprintf(os.Stderr, "depmesh-ai %s serving on %s (policy: %v, audit: %q)\n",
		gate.Version, addr, g.Policy != nil, g.Audit.Path)
	if err := api.ListenAndServe(addr, g); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	return 0
}

func render(o *gate.Outcome) string {
	v := o.Verdict
	badge := map[vet.Advice]string{vet.Adopt: "✓", vet.Caution: "⚠", vet.Reject: "✗"}
	out := fmt.Sprintf("%s %s  %s:%s  (score %d/100)\n",
		badge[v.Advice], v.Advice, v.Ecosystem, v.Package, v.Score)
	if v.Version != "" {
		out += fmt.Sprintf("  version: %s\n", v.Version)
	}
	if v.LatestVersion != "" {
		out += fmt.Sprintf("  latest: %s\n", v.LatestVersion)
	}
	for _, signal := range v.Signals {
		marker := "·"
		delta := ""
		if signal.Delta < 0 {
			marker = "-"
			delta = fmt.Sprintf(" [%+d]", signal.Delta)
		}
		out += fmt.Sprintf("  %s %s%s: %s\n", marker, signal.Name, delta, signal.Reason)
	}
	for _, degraded := range v.Degraded {
		out += fmt.Sprintf("  ? unavailable source: %s\n", degraded)
	}
	if p := o.Policy; p != nil {
		switch {
		case p.Exception != nil:
			out += fmt.Sprintf("  policy: ALLOWED by exception (%s", p.Exception.Reason)
			if p.Exception.Expires != "" {
				out += ", expires " + p.Exception.Expires
			}
			out += ")\n"
		case p.Allowed:
			out += "  policy: ALLOWED\n"
		default:
			out += "  policy: BLOCKED\n"
			for _, violation := range p.Violations {
				out += fmt.Sprintf("    ! %s\n", violation)
			}
		}
	}
	return out
}
