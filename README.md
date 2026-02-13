# go-callgraph-neo4j

Accurate call graph for Go projects → Neo4j.

Uses **go/packages + SSA + VTA** (Variable Type Analysis) instead of Tree-sitter. This provides **type-accurate** CALLS relationships without false positives on same-named methods of different types.

## What it extracts

| Nodes | Description |
|---|---|
| `GoPackage` | Go packages in the project |
| `GoStruct` | All structs with fields |
| `GoInterface` | All interfaces with method counts |
| `GoFunc` | All functions and methods |

| Edges | Description |
|---|---|
| `ACCURATE_CALLS` | Precise calls with type resolution (not by name!) |
| `IMPLEMENTS` | Which structs implement which interfaces |
| `HAS_METHOD` | Struct → its methods |
| `IN_PACKAGE` | Any entity → its package |

The `ACCURATE_CALLS` relationship has an `is_dynamic: true/false` property indicating whether it's a direct call or through an interface.

## Installation

```bash
cd go-callgraph-neo4j
go mod tidy

# Build using Make
make build_native

# Or build manually
go build -mod=vendor -o ./go-callgraph-neo4j ./
```

## Usage

```bash
# Basic usage (from Go project root)
./go-callgraph-neo4j \
  --dir /path/to/your/go-project \
  --neo4j-pass your-secure-password \
  --clean

# All flags
./go-callgraph-neo4j \
  --dir /path/to/your/go-project \
  --neo4j-uri bolt://localhost:7687 \
  --neo4j-user neo4j \
  --neo4j-pass your-secure-password \
  --clean  # delete old Go* nodes before loading
```

## Key Cypher queries

```cypher
-- All project packages
MATCH (p:GoPackage) RETURN p.name, p.import_path ORDER BY p.import_path

-- Who calls CreateOrder (ACCURATE, with type resolution)
MATCH (caller:GoFunc)-[:ACCURATE_CALLS]->(f:GoFunc)
WHERE f.name = 'CreateOrder'
RETURN caller.full_name, caller.file, caller.line

-- God functions (most outgoing calls)
MATCH (f:GoFunc)-[r:ACCURATE_CALLS]->(target)
RETURN f.full_name, count(target) as calls
ORDER BY calls DESC LIMIT 20

-- Which structs implement an interface
MATCH (s:GoStruct)-[:IMPLEMENTS]->(i:GoInterface)
RETURN s.name, s.package, i.name, i.package

-- Dynamic calls (through interface)
MATCH (f:GoFunc)-[r:ACCURATE_CALLS {is_dynamic: true}]->(target)
RETURN f.full_name, target.full_name, r.site

-- Struct methods
MATCH (s:GoStruct {name: 'OrderService'})-[:HAS_METHOD]->(m:GoFunc)
RETURN m.name, m.file, m.line

-- Transitive function dependencies (depth 3)
MATCH path = (f:GoFunc)-[:ACCURATE_CALLS*1..3]->(target:GoFunc)
WHERE f.name = 'CreateOrder'
RETURN path

-- Package and all its dependencies
MATCH (f:GoFunc)-[:IN_PACKAGE]->(p:GoPackage)
WHERE p.import_path CONTAINS 'internal/services'
MATCH (f)-[:ACCURATE_CALLS]->(callee:GoFunc)-[:IN_PACKAGE]->(dep:GoPackage)
WHERE dep <> p
RETURN DISTINCT p.import_path, dep.import_path
```

## Coexistence with CGC

This tool creates nodes labeled `GoPackage`, `GoStruct`, `GoInterface`, `GoFunc` and relationships `ACCURATE_CALLS`, `IMPLEMENTS`, `HAS_METHOD`, `IN_PACKAGE` — they **do not overlap** with CGC labels (`File`, `Function`, `Class`, `Module`, `Variable`, `Parameter`, `CALLS`, `CONTAINS`).

You can use both graphs simultaneously in one Neo4j database:
- **CGC graph** (`CALLS`): quick overview, file navigation
- **Accurate graph** (`ACCURATE_CALLS`): precise dependencies for impact analysis

### CLAUDE.md recommendation

```markdown
## Graphs in Neo4j
The database contains two graphs:
1. CGC (Tree-sitter): labels File, Function, Class. CALLS relationships are IMPRECISE (by name).
2. Go-accurate (SSA/VTA): labels GoFunc, GoStruct, GoInterface. ACCURATE_CALLS relationships are PRECISE.

RULE: for impact analysis and finding callers ALWAYS use ACCURATE_CALLS.
Use CGC graph CALLS only for file navigation.
```

## VTA algorithm

VTA (Variable Type Analysis) is an algorithm from `golang.org/x/tools/go/callgraph/vta`. It tracks which concrete types can be assigned to interface-typed variables and builds a call graph based on this analysis.

Alternatives: `static` (direct calls only), `cha` (Class Hierarchy Analysis — less precise), `rta` (Rapid Type Analysis — similar to VTA). VTA is the best balance of precision and speed for Go.
