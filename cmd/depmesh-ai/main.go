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
	"github.com/jhberges/depmesh-ai/internal/apikey"
	"github.com/jhberges/depmesh-ai/internal/audit"
	"github.com/jhberges/depmesh-ai/internal/bytesize"
	"github.com/jhberges/depmesh-ai/internal/gate"
	"github.com/jhberges/depmesh-ai/internal/mcp"
	"github.com/jhberges/depmesh-ai/internal/sources"
	"github.com/jhberges/depmesh-ai/internal/vet"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  depmesh-ai vet [--json] [--no-enrich] [--policy FILE] [--audit-log FILE] [--upstream URL] <ecosystem> <package>
  depmesh-ai serve [--policy FILE] [--audit-log FILE] [--upstream URL] [--upstream-key KEY]
  depmesh-ai api [--listen ADDR] [--policy FILE] [--audit-log FILE] [--api-key KEY|off]

audit rotation (any surface): --audit-max-size 100MB, --audit-keep 5.
0 means never rotate; both default from the policy file when set there.

ecosystems: npm | pypi | maven (maven packages: groupId:artifactId)

policy is auto-discovered from ./depmesh.policy.json or $DEPMESH_POLICY;
--policy makes it explicit and errors when the file is missing.

--upstream (or $DEPMESH_UPSTREAM) delegates the decision to a central
"depmesh-ai api" instance, so this machine needs no registry access and the
gate's policy and audit log apply. It never falls back to direct registry
access when that gate is unreachable. Pair it with --upstream-key (or
$DEPMESH_UPSTREAM_KEY) for the key that gate requires.

"api" requires an API key. It reads --api-key, then $DEPMESH_API_KEY, and
generates one when neither is set, printing it on startup. --api-key off
serves without authentication.`)
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

type upstreamOptions struct {
	url *string
	key *string
}

func upstreamFlags(flags *flag.FlagSet) upstreamOptions {
	return upstreamOptions{
		// Backquotes name the placeholder in flag's usage output — hence `URL`.
		url: flags.String("upstream", "",
			"delegate to a central depmesh-ai api instance at this `URL` instead of contacting registries (default: $DEPMESH_UPSTREAM)"),
		key: flags.String("upstream-key", "",
			"API `key` that gate requires (default: $DEPMESH_UPSTREAM_KEY)"),
	}
}

// resolve prefers flags, then the environment. Only the two developer-facing
// surfaces call it: `api` is the far end of the delegation and must never
// chain onward.
func (o upstreamOptions) resolve() gate.Upstream {
	up := gate.Upstream{URL: *o.url, Key: *o.key}
	if up.URL == "" {
		up.URL = os.Getenv("DEPMESH_UPSTREAM")
	}
	if up.Key == "" {
		up.Key = os.Getenv(apikey.UpstreamEnvVar)
	}
	return up
}

// resolveAPIKey decides what the served gate requires. Absent configuration
// generates one rather than serving open: the audit log is a write path to
// disk, and an unauthenticated write path is a denial-of-service waiting to
// be found. Turning it off has to be said out loud.
func resolveAPIKey(flagValue string) (key string, generated bool) {
	if flagValue == "" {
		flagValue = os.Getenv(apikey.EnvVar)
	}
	if strings.EqualFold(strings.TrimSpace(flagValue), apikey.Off) {
		return "", false
	}
	if flagValue != "" {
		return flagValue, false
	}
	generatedKey, err := apikey.Generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	return generatedKey, true
}

func buildGate(options gateOptions, up gate.Upstream) *gate.Gate {
	g, err := gate.New(*options.policyPath, *options.auditLog, up)
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
	if g.Upstream.URL != "" {
		if g.Policy != nil {
			fmt.Fprintln(os.Stderr, "note: --upstream is set; the local policy file is ignored, the gate's policy applies")
		}
		if g.Audit.Path != "" {
			fmt.Fprintln(os.Stderr, "note: --upstream is set; decisions are audited by the gate, not locally")
		}
	}
	return g
}

func runVet(args []string) int {
	flags := flag.NewFlagSet("vet", flag.ExitOnError)
	asJSON := flags.Bool("json", false, "machine-readable output")
	noEnrich := flags.Bool("no-enrich", false, "skip deps.dev enrichment (advisories)")
	options := gateFlags(flags)
	up := upstreamFlags(flags)
	_ = flags.Parse(args)
	if flags.NArg() != 2 {
		usage()
	}
	g := buildGate(options, up.resolve())

	outcome, err := g.Vet(audit.Local("cli"), flags.Arg(0), flags.Arg(1), !*noEnrich)
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
	up := upstreamFlags(flags)
	_ = flags.Parse(args)
	g := buildGate(options, up.resolve())
	if err := mcp.Serve(g, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	return 0
}

func runAPI(args []string) int {
	flags := flag.NewFlagSet("api", flag.ExitOnError)
	listen := flags.String("listen", ":8385", "listen address")
	apiKeyFlag := flags.String("api-key", "",
		"API `key` clients must present, or \"off\" to serve without one (default: $DEPMESH_API_KEY, else generated)")
	options := gateFlags(flags)
	_ = flags.Parse(args)
	g := buildGate(options, gate.Upstream{})
	key, generated := resolveAPIKey(*apiKeyFlag)

	fmt.Fprintf(os.Stderr, "depmesh-ai %s serving on %s (policy: %v, audit: %q)\n",
		gate.Version, *listen, g.Policy != nil, g.Audit.Path)
	switch {
	case generated:
		// Printed once, to stderr, and never again: a restart makes a new one,
		// which is why anything long-lived should set the key explicitly.
		fmt.Fprintf(os.Stderr, "API key (generated for this run): %s\n", key)
		fmt.Fprintf(os.Stderr, "  clients: depmesh-ai serve --upstream http://HOST%s --upstream-key %s\n", *listen, key)
		fmt.Fprintf(os.Stderr, "  set $%s to keep it stable across restarts, or --api-key off to disable auth\n",
			apikey.EnvVar)
	case key == "":
		fmt.Fprintln(os.Stderr, "WARNING: --api-key off — anyone who can reach this port can query it and grow its audit log")
	default:
		fmt.Fprintln(os.Stderr, "API key: configured")
	}
	if err := api.ListenAndServe(*listen, g, key); err != nil {
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
