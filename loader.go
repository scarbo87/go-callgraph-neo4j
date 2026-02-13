package main

import (
	"context"
	"fmt"
	"log"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Neo4jLoader loads collected call-graph data into a Neo4j database
// using batch UNWIND queries.
type Neo4jLoader struct {
	driver neo4j.DriverWithContext
	ctx    context.Context
}

// NewNeo4jLoader connects to Neo4j and returns a ready-to-use loader.
func NewNeo4jLoader(ctx context.Context, uri, user, password string) (*Neo4jLoader, error) {
	driver, err := neo4j.NewDriverWithContext(uri, neo4j.BasicAuth(user, password, ""))
	if err != nil {
		return nil, fmt.Errorf("failed to create neo4j driver: %w", err)
	}
	return &Neo4jLoader{driver: driver, ctx: ctx}, nil
}

// Close releases the underlying Neo4j driver resources.
func (l *Neo4jLoader) Close() {
	l.driver.Close(l.ctx)
}

// runCypher runs a single Cypher statement with optional parameters.
func (l *Neo4jLoader) runCypher(cypher string, params map[string]any) error {
	_, err := neo4j.ExecuteQuery(l.ctx, l.driver, cypher, params, neo4j.EagerResultTransformer)
	return err
}

// CleanGraph removes all previously loaded call-graph nodes and relationships.
func (l *Neo4jLoader) CleanGraph() error {
	log.Println("Cleaning existing accurate graph data...")
	queries := []string{
		"MATCH ()-[r:ACCURATE_CALLS]->() DELETE r",
		"MATCH ()-[r:IMPLEMENTS]->() DELETE r",
		"MATCH ()-[r:IN_PACKAGE]->() DELETE r",
		"MATCH ()-[r:HAS_METHOD]->() DELETE r",
		"MATCH (n:GoPackage) DETACH DELETE n",
		"MATCH (n:GoFunc) DETACH DELETE n",
		"MATCH (n:GoStruct) DETACH DELETE n",
		"MATCH (n:GoInterface) DETACH DELETE n",
	}
	for _, q := range queries {
		if err := l.runCypher(q, nil); err != nil {
			return err
		}
	}
	return nil
}

// CreateIndexes ensures the required Neo4j indexes exist.
func (l *Neo4jLoader) CreateIndexes() error {
	log.Println("Creating indexes...")
	indexes := []string{
		"CREATE INDEX go_pkg_path IF NOT EXISTS FOR (n:GoPackage) ON (n.import_path)",
		"CREATE INDEX go_func_fullname IF NOT EXISTS FOR (n:GoFunc) ON (n.full_name)",
		"CREATE INDEX go_struct_key IF NOT EXISTS FOR (n:GoStruct) ON (n.key)",
		"CREATE INDEX go_iface_key IF NOT EXISTS FOR (n:GoInterface) ON (n.key)",
	}
	for _, q := range indexes {
		if err := l.runCypher(q, nil); err != nil {
			return err
		}
	}
	return nil
}

// LoadPackages upserts GoPackage nodes.
func (l *Neo4jLoader) LoadPackages(pkgs map[string]*PackageNode) error {
	log.Printf("Loading %d packages...", len(pkgs))
	batch := make([]map[string]any, 0, len(pkgs))
	for _, p := range pkgs {
		batch = append(batch, map[string]any{
			"path": p.ImportPath,
			"name": p.Name,
			"dir":  p.Dir,
		})
	}
	return l.runCypher(
		`UNWIND $batch AS row
		 MERGE (n:GoPackage {import_path: row.path})
		 SET n.name = row.name, n.dir = row.dir`,
		map[string]any{"batch": batch},
	)
}

// LoadStructs upserts GoStruct nodes and links them to their packages.
func (l *Neo4jLoader) LoadStructs(structs map[string]*StructNode) error {
	log.Printf("Loading %d structs...", len(structs))
	batch := make([]map[string]any, 0, len(structs))
	for key, s := range structs {
		batch = append(batch, map[string]any{
			"key": key, "name": s.Name, "pkg": s.Package,
			"file": s.File, "line": s.Line, "exported": s.Exported,
			"fields": s.FieldCount,
		})
	}
	return l.runCypher(
		`UNWIND $batch AS row
		 MERGE (n:GoStruct {key: row.key})
		 SET n.name = row.name, n.package = row.pkg, n.file = row.file,
		     n.line = row.line, n.exported = row.exported, n.field_count = row.fields
		 WITH n, row
		 MATCH (p:GoPackage {import_path: row.pkg})
		 MERGE (n)-[:IN_PACKAGE]->(p)`,
		map[string]any{"batch": batch},
	)
}

// LoadInterfaces upserts GoInterface nodes and links them to their packages.
func (l *Neo4jLoader) LoadInterfaces(ifaces map[string]*InterfaceNode) error {
	log.Printf("Loading %d interfaces...", len(ifaces))
	batch := make([]map[string]any, 0, len(ifaces))
	for key, i := range ifaces {
		batch = append(batch, map[string]any{
			"key": key, "name": i.Name, "pkg": i.Package,
			"file": i.File, "line": i.Line, "exported": i.Exported,
			"methods": i.Methods,
		})
	}
	return l.runCypher(
		`UNWIND $batch AS row
		 MERGE (n:GoInterface {key: row.key})
		 SET n.name = row.name, n.package = row.pkg, n.file = row.file,
		     n.line = row.line, n.exported = row.exported, n.method_count = row.methods
		 WITH n, row
		 MATCH (p:GoPackage {import_path: row.pkg})
		 MERGE (n)-[:IN_PACKAGE]->(p)`,
		map[string]any{"batch": batch},
	)
}

// LoadFuncs upserts GoFunc nodes, links them to packages, and creates
// HAS_METHOD edges from structs to their methods.
func (l *Neo4jLoader) LoadFuncs(funcs map[string]*FuncNode) error {
	log.Printf("Loading %d functions...", len(funcs))
	batch := make([]map[string]any, 0, len(funcs))
	for _, fn := range funcs {
		batch = append(batch, map[string]any{
			"fullname": fn.FullName, "name": fn.Name, "pkg": fn.Package,
			"file": fn.File, "line": fn.Line, "exported": fn.Exported,
			"receiver": fn.Receiver, "is_method": fn.IsMethod,
		})
	}
	err := l.runCypher(
		`UNWIND $batch AS row
		 MERGE (n:GoFunc {full_name: row.fullname})
		 SET n.name = row.name, n.package = row.pkg, n.file = row.file,
		     n.line = row.line, n.exported = row.exported,
		     n.receiver = row.receiver, n.is_method = row.is_method
		 WITH n, row
		 MATCH (p:GoPackage {import_path: row.pkg})
		 MERGE (n)-[:IN_PACKAGE]->(p)`,
		map[string]any{"batch": batch},
	)
	if err != nil {
		return err
	}

	// HAS_METHOD edges (struct -> method)
	methods := make([]map[string]any, 0)
	for _, fn := range funcs {
		if fn.IsMethod && fn.Receiver != "" {
			methods = append(methods, map[string]any{
				"skey":     fn.Package + "." + fn.Receiver,
				"fullname": fn.FullName,
			})
		}
	}
	if len(methods) > 0 {
		return l.runCypher(
			`UNWIND $batch AS row
			 MATCH (s:GoStruct {key: row.skey}), (f:GoFunc {full_name: row.fullname})
			 MERGE (s)-[:HAS_METHOD]->(f)`,
			map[string]any{"batch": methods},
		)
	}
	return nil
}

// LoadCalls upserts ACCURATE_CALLS relationships between GoFunc nodes.
func (l *Neo4jLoader) LoadCalls(calls []CallEdge) error {
	log.Printf("Loading %d call edges...", len(calls))
	batch := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		batch = append(batch, map[string]any{
			"caller":  c.CallerFullName,
			"callee":  c.CalleeFullName,
			"dynamic": c.IsDynamic,
			"site":    c.Site,
		})
	}
	return l.runCypher(
		`UNWIND $batch AS row
		 MERGE (caller:GoFunc {full_name: row.caller})
		 MERGE (callee:GoFunc {full_name: row.callee})
		 MERGE (caller)-[r:ACCURATE_CALLS]->(callee)
		 SET r.is_dynamic = row.dynamic, r.site = row.site`,
		map[string]any{"batch": batch},
	)
}

// LoadImplements upserts IMPLEMENTS relationships between GoStruct and GoInterface nodes.
func (l *Neo4jLoader) LoadImplements(impls []ImplementsEdge) error {
	log.Printf("Loading %d implements edges...", len(impls))
	batch := make([]map[string]any, 0, len(impls))
	for _, e := range impls {
		batch = append(batch, map[string]any{
			"struct": e.Struct,
			"iface":  e.Interface,
		})
	}
	return l.runCypher(
		`UNWIND $batch AS row
		 MATCH (s:GoStruct {key: row.struct}), (i:GoInterface {key: row.iface})
		 MERGE (s)-[:IMPLEMENTS]->(i)`,
		map[string]any{"batch": batch},
	)
}
