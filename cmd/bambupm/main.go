// bambupm is a minimal CLI over internal/service. It only wires the
// read/reversible operations (scan, export) to a real Bambu Studio
// directory by default — rebind/publish need an explicit --allow-publish
// flag, since a mistake there writes into a live, shared Bambu Studio
// installation (see internal/bambuadapter's package doc).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/syscod3/bambu-profile-manager/internal/bambuadapter"
	"github.com/syscod3/bambu-profile-manager/internal/parser"
	"github.com/syscod3/bambu-profile-manager/internal/resolver"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "scan":
		cmdScan(os.Args[2:])
	case "resolve":
		cmdResolve(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `bambupm — Bambu Studio profile manager

Usage:
  bambupm scan --dir <bambu-user-filament-dir>
      List profiles found in a Bambu Studio user filament directory.

  bambupm resolve --dir <dir> --name "<profile name>"
      Resolve a profile's full inheritance chain and print the effective
      (flattened) fields as JSON. --dir may be passed more than once to
      search several directories (e.g. user + system profile dirs).

Export/import/rebind/publish are available via internal/service and
internal/bundle for now — no CLI surface yet (see
openspec/changes/init-profile-manager/tasks.md, unit 12).`)
}

func cmdScan(args []string) {
	dir := flagValue(args, "--dir")
	if dir == "" {
		fmt.Fprintln(os.Stderr, "scan: --dir is required")
		os.Exit(2)
	}
	a := &bambuadapter.LocalAdapter{Dir: dir}
	found, err := a.Discover(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "scan:", err)
		os.Exit(1)
	}
	for _, p := range found {
		fmt.Println(p.Name)
	}
}

func cmdResolve(args []string) {
	dirs := flagValues(args, "--dir")
	name := flagValue(args, "--name")
	if len(dirs) == 0 || name == "" {
		fmt.Fprintln(os.Stderr, "resolve: --dir (>=1) and --name are required")
		os.Exit(2)
	}

	set := resolver.Set{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "resolve: read %s: %v\n", dir, err)
			os.Exit(1)
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			p, err := parser.Load(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			set[p.Name] = p
		}
	}

	leaf, ok := set[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "resolve: %q not found under given --dir(s)\n", name)
		os.Exit(1)
	}
	effective, chain, err := resolver.Resolve(set, leaf)
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "chain: %v\n", chain)
	b, err := json.MarshalIndent(effective.Fields, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "resolve: marshal:", err)
		os.Exit(1)
	}
	fmt.Println(string(b))
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func flagValues(args []string, name string) []string {
	var out []string
	for i, a := range args {
		if a == name && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}
