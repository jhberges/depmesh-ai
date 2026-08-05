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

	"github.com/jhberges/depmesh-ai/internal/api"
	"github.com/jhberges/depmesh-ai/internal/gate"
	"github.com/jhberges/depmesh-ai/internal/mcp"
	"github.com/jhberges/depmesh-ai/internal/sources"
	"github.com/jhberges/depmesh-ai/internal/vet"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  depmesh-ai vet [--json] [--no-enrich] [--policy FILE] [--audit-log FILE] <ecosystem> <package>
  depmesh-ai serve [--policy FILE] [--audit-log FILE]
  depmesh-ai api [--listen ADDR] [--policy FILE] [--audit-log FILE]

ecosystems: npm | pypi | maven (maven packages: groupId:artifactId)

policy is auto-discovered from ./depmesh.policy.json or $DEPMESH_POLICY;
--policy makes it explicit and errors when the file is missing.`)
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

func gateFlags(flags *flag.FlagSet) (policyPath, auditLog *string) {
	policyPath = flags.String("policy", "", "policy file (default: $DEPMESH_POLICY or ./depmesh.policy.json)")
	auditLog = flags.String("audit-log", "", "append JSONL decision records to this file (overrides policy)")
	return
}

func buildGate(policyPath, auditLog string) *gate.Gate {
	g, err := gate.New(policyPath, auditLog)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	return g
}

func runVet(args []string) int {
	flags := flag.NewFlagSet("vet", flag.ExitOnError)
	asJSON := flags.Bool("json", false, "machine-readable output")
	noEnrich := flags.Bool("no-enrich", false, "skip deps.dev enrichment (advisories)")
	policyPath, auditLog := gateFlags(flags)
	_ = flags.Parse(args)
	if flags.NArg() != 2 {
		usage()
	}
	g := buildGate(*policyPath, *auditLog)

	outcome, err := g.Vet("cli", flags.Arg(0), flags.Arg(1), !*noEnrich)
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
	policyPath, auditLog := gateFlags(flags)
	_ = flags.Parse(args)
	g := buildGate(*policyPath, *auditLog)
	if err := mcp.Serve(g, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	return 0
}

func runAPI(args []string) int {
	flags := flag.NewFlagSet("api", flag.ExitOnError)
	listen := flags.String("listen", ":8385", "listen address")
	policyPath, auditLog := gateFlags(flags)
	_ = flags.Parse(args)
	g := buildGate(*policyPath, *auditLog)
	fmt.Fprintf(os.Stderr, "depmesh-ai %s serving on %s (policy: %v, audit: %q)\n",
		gate.Version, *listen, g.Policy != nil, g.AuditLog)
	if err := api.ListenAndServe(*listen, g); err != nil {
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
