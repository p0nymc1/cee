// Command cee is the CEE command-line tool. Today it exposes a single
// subcommand, `validate`, which statically checks a domain manifest against
// the structural rules and reference integrity a plugin must satisfy before
// it can be published to the community catalog -- turning the normative
// handbook's red lines into an automated gate so a contributor never has to
// wait on a human reviewer to know their manifest is well-formed.
package main

import (
	"fmt"
	"os"

	"cee/manifest"
	"cee/stdlib"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "validate":
		os.Exit(runValidate(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "cee: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `cee - Cognitive Execution Engine tooling

usage:
  cee validate <manifest.json>   statically check a domain manifest

`)
}

func runValidate(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: cee validate <manifest.json>")
		return 2
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "cee validate: %v\n", err)
		return 2
	}

	report := manifest.Validate(data, stdlib.Default())
	fmt.Println(report.String())
	if !report.OK() {
		return 1
	}
	return 0
}
