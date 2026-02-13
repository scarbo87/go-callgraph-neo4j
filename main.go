package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

func main() {
	var (
		neo4jURI  = flag.String("neo4j-uri", "bolt://localhost:7687", "Neo4j bolt URI")
		neo4jUser = flag.String("neo4j-user", "neo4j", "Neo4j username")
		neo4jPass = flag.String("neo4j-pass", "", "Neo4j password")
		clean     = flag.Bool("clean", false, "Clean existing accurate graph data before loading")
		dir       = flag.String("dir", ".", "Project root directory")
	)
	flag.Parse()

	if *neo4jPass == "" {
		fmt.Fprintln(os.Stderr, "Error: --neo4j-pass is required")
		flag.Usage()
		os.Exit(1)
	}

	// Resolve absolute path and module name.
	absDir, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatal(err)
	}

	// Detect module path from go.mod.
	modulePath, err := detectModulePath(absDir)
	if err != nil {
		log.Fatalf("Cannot detect Go module: %v", err)
	}
	log.Printf("Module: %s", modulePath)
	log.Printf("Dir: %s", absDir)

	// Load packages.
	log.Println("Loading packages (this may take a minute)...")
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedTypesSizes,
		Dir: absDir,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		log.Fatalf("Failed to load packages: %v", err)
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		log.Printf("Warning: %d package errors (continuing anyway)", n)
	}
	log.Printf("Loaded %d packages", len(pkgs))

	// Collect data.
	collector := NewCollector(modulePath)

	log.Println("Collecting types (structs, interfaces, functions)...")
	collector.CollectTypes(pkgs)

	log.Println("Building SSA and call graph (VTA)...")
	collector.CollectCallGraph(pkgs)

	log.Println("Checking interface implementations...")
	collector.CollectImplementsFromPackages(pkgs)

	// Stats.
	log.Printf("Collected: %d packages, %d structs, %d interfaces, %d functions, %d calls, %d implements",
		len(collector.Packages), len(collector.Structs), len(collector.Interfaces),
		len(collector.Funcs), len(collector.Calls), len(collector.Implements))

	// Load into Neo4j.
	ctx := context.Background()
	loader, err := NewNeo4jLoader(ctx, *neo4jURI, *neo4jUser, *neo4jPass)
	if err != nil {
		log.Fatal(err)
	}
	defer loader.Close()

	if *clean {
		if err := loader.CleanGraph(); err != nil {
			log.Fatal(err)
		}
	}

	if err := loader.CreateIndexes(); err != nil {
		log.Fatal(err)
	}
	if err := loader.LoadPackages(collector.Packages); err != nil {
		log.Fatal(err)
	}
	if err := loader.LoadStructs(collector.Structs); err != nil {
		log.Fatal(err)
	}
	if err := loader.LoadInterfaces(collector.Interfaces); err != nil {
		log.Fatal(err)
	}
	if err := loader.LoadFuncs(collector.Funcs); err != nil {
		log.Fatal(err)
	}
	if err := loader.LoadCalls(collector.Calls); err != nil {
		log.Fatal(err)
	}
	if err := loader.LoadImplements(collector.Implements); err != nil {
		log.Fatal(err)
	}

	log.Println("Done! Graph loaded into Neo4j.")
	log.Println("")
	log.Println("Useful Cypher queries:")
	log.Println("  // All packages")
	log.Println("  MATCH (p:GoPackage) RETURN p.name, p.import_path ORDER BY p.import_path")
	log.Println("")
	log.Println("  // Functions with most outgoing calls")
	log.Println("  MATCH (f:GoFunc)-[r:ACCURATE_CALLS]->(target) RETURN f.full_name, count(target) as calls ORDER BY calls DESC LIMIT 20")
	log.Println("")
	log.Println("  // Who calls a specific function (with type-accurate resolution)")
	log.Println("  MATCH (caller:GoFunc)-[:ACCURATE_CALLS]->(f:GoFunc {name: 'CreateOrder'}) RETURN caller.full_name, f.full_name")
	log.Println("")
	log.Println("  // Structs implementing an interface")
	log.Println("  MATCH (s:GoStruct)-[:IMPLEMENTS]->(i:GoInterface) RETURN s.name, i.name")
	log.Println("")
	log.Println("  // Dynamic (interface) calls")
	log.Println("  MATCH (f:GoFunc)-[r:ACCURATE_CALLS {is_dynamic: true}]->(target) RETURN f.full_name, target.full_name, r.site")
}

// detectModulePath reads the go.mod file in dir and returns the module path.
func detectModulePath(dir string) (string, error) {
	gomod := filepath.Join(dir, "go.mod")
	data, err := os.ReadFile(gomod)
	if err != nil {
		return "", fmt.Errorf("cannot read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module")), nil
		}
	}
	return "", fmt.Errorf("module directive not found in go.mod")
}
