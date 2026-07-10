// depmesh-ai: vet open-source dependencies before adopting them.
//
//	depmesh-ai vet npm express
//	depmesh-ai vet maven org.apache.commons:commons-lang3 --json
//	depmesh-ai serve          # MCP server on stdio
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/jhberges/depmesh-ai/internal/mcp"
	"github.com/jhberges/depmesh-ai/internal/sources"
	"github.com/jhberges/depmesh-ai/internal/vet"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  depmesh-ai vet [--json] [--no-enrich] <ecosystem> <package>
  depmesh-ai serve

ecosystems: npm | pypi | maven (maven packages: groupId:artifactId)`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "serve":
		if err := mcp.Serve(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(2)
		}
	case "vet":
		os.Exit(runVet(os.Args[2:]))
	default:
		usage()
	}
}

func runVet(args []string) int {
	flags := flag.NewFlagSet("vet", flag.ExitOnError)
	asJSON := flags.Bool("json", false, "machine-readable output")
	noEnrich := flags.Bool("no-enrich", false, "skip deps.dev enrichment (advisories)")
	_ = flags.Parse(args)
	if flags.NArg() != 2 {
		usage()
	}

	verdict, err := vet.Vet(flags.Arg(0), flags.Arg(1), !*noEnrich)
	var unavailable *sources.UnavailableError
	switch {
	case errors.As(err, &unavailable):
		fmt.Fprintln(os.Stderr, "error: registry unreachable, cannot vet:", err)
		return 2
	case err != nil:
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}

	if *asJSON {
		fmt.Println(verdict.JSON())
	} else {
		fmt.Print(render(verdict))
	}
	if verdict.Advice == vet.Reject {
		return 1
	}
	return 0
}

func render(v *vet.Verdict) string {
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
	return out
}
