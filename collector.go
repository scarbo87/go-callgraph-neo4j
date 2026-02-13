package main

import (
	"fmt"
	"go/types"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// Collector gathers type, call graph, and interface implementation data
// from Go packages using static analysis.
type Collector struct {
	RootModule string

	Packages   map[string]*PackageNode
	Structs    map[string]*StructNode
	Interfaces map[string]*InterfaceNode
	Funcs      map[string]*FuncNode
	Calls      []CallEdge
	Implements []ImplementsEdge
}

// NewCollector creates a Collector scoped to the given root module path.
func NewCollector(rootModule string) *Collector {
	return &Collector{
		RootModule: rootModule,
		Packages:   make(map[string]*PackageNode),
		Structs:    make(map[string]*StructNode),
		Interfaces: make(map[string]*InterfaceNode),
		Funcs:      make(map[string]*FuncNode),
	}
}

// isProjectPackage reports whether pkgPath belongs to the analysed module.
func (c *Collector) isProjectPackage(pkgPath string) bool {
	return strings.HasPrefix(pkgPath, c.RootModule)
}

// relPath strips the module prefix from a full file or package path,
// returning a path relative to the project root.
func (c *Collector) relPath(fullPath string) string {
	if idx := strings.Index(fullPath, c.RootModule); idx >= 0 {
		rest := fullPath[idx+len(c.RootModule):]
		if len(rest) > 0 && rest[0] == '/' {
			return rest[1:]
		}
		return rest
	}
	return fullPath
}

// CollectTypes walks all packages and extracts structs, interfaces, and functions.
func (c *Collector) CollectTypes(pkgs []*packages.Package) {
	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		if !c.isProjectPackage(pkg.PkgPath) {
			return
		}

		// Package node
		c.Packages[pkg.PkgPath] = &PackageNode{
			ImportPath: pkg.PkgPath,
			Name:       pkg.Name,
			Dir:        c.relPath(pkg.PkgPath),
		}

		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			pos := pkg.Fset.Position(obj.Pos())
			file := c.relPath(pos.Filename)

			switch o := obj.(type) {
			case *types.TypeName:
				switch t := o.Type().Underlying().(type) {
				case *types.Struct:
					key := pkg.PkgPath + "." + name
					c.Structs[key] = &StructNode{
						Name:       name,
						Package:    pkg.PkgPath,
						File:       file,
						Line:       pos.Line,
						Exported:   o.Exported(),
						FieldCount: t.NumFields(),
					}
				case *types.Interface:
					key := pkg.PkgPath + "." + name
					c.Interfaces[key] = &InterfaceNode{
						Name:     name,
						Package:  pkg.PkgPath,
						File:     file,
						Line:     pos.Line,
						Exported: o.Exported(),
						Methods:  t.NumMethods(),
					}
				}

			case *types.Func:
				sig := o.Type().(*types.Signature)
				fn := &FuncNode{
					Name:     name,
					FullName: pkg.PkgPath + "." + name,
					Package:  pkg.PkgPath,
					File:     file,
					Line:     pos.Line,
					Exported: o.Exported(),
				}
				if recv := sig.Recv(); recv != nil {
					recvType := recv.Type()
					if ptr, ok := recvType.(*types.Pointer); ok {
						recvType = ptr.Elem()
					}
					if named, ok := recvType.(*types.Named); ok {
						fn.Receiver = named.Obj().Name()
						fn.IsMethod = true
						fn.FullName = pkg.PkgPath + "." + fn.Receiver + "." + name
					}
				}
				c.Funcs[fn.FullName] = fn
			}
		}

		// Also collect methods from named types (methods defined on structs).
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if tn, ok := obj.(*types.TypeName); ok {
				if named, ok := tn.Type().(*types.Named); ok {
					for i := 0; i < named.NumMethods(); i++ {
						m := named.Method(i)
						pos := pkg.Fset.Position(m.Pos())
						file := c.relPath(pos.Filename)
						fn := &FuncNode{
							Name:     m.Name(),
							FullName: pkg.PkgPath + "." + name + "." + m.Name(),
							Package:  pkg.PkgPath,
							File:     file,
							Line:     pos.Line,
							Exported: m.Exported(),
							Receiver: name,
							IsMethod: true,
						}
						c.Funcs[fn.FullName] = fn
					}
				}
			}
		}
	})
}

// CollectCallGraph builds SSA, runs VTA, and extracts CALLS edges.
func (c *Collector) CollectCallGraph(pkgs []*packages.Package) {
	// Build SSA
	prog, ssaPkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	for _, p := range ssaPkgs {
		if p != nil {
			p.Build()
		}
	}

	// Run VTA (Variable Type Analysis) -- best balance of precision vs speed.
	cg := vta.CallGraph(ssautil.AllFunctions(prog), nil)

	// Extract edges -- only between project functions.
	callgraph.GraphVisitEdges(cg, func(edge *callgraph.Edge) error {
		caller := edge.Caller.Func
		callee := edge.Callee.Func

		if caller.Pkg == nil || callee.Pkg == nil {
			return nil
		}

		callerPkg := caller.Pkg.Pkg.Path()
		calleePkg := callee.Pkg.Pkg.Path()

		if !c.isProjectPackage(callerPkg) && !c.isProjectPackage(calleePkg) {
			return nil
		}

		// Build full names matching our FuncNode naming.
		callerName := buildSSAFuncName(caller)
		calleeName := buildSSAFuncName(callee)

		site := ""
		if edge.Site != nil {
			pos := prog.Fset.Position(edge.Site.Pos())
			site = fmt.Sprintf("%s:%d", c.relPath(pos.Filename), pos.Line)
		}

		c.Calls = append(c.Calls, CallEdge{
			CallerFullName: callerName,
			CalleeFullName: calleeName,
			IsDynamic:      edge.Site != nil && edge.Site.Common().IsInvoke(),
			Site:           site,
		})

		// Register functions discovered during call graph analysis.
		if _, ok := c.Funcs[callerName]; !ok && c.isProjectPackage(callerPkg) {
			c.Funcs[callerName] = &FuncNode{
				Name:     caller.Name(),
				FullName: callerName,
				Package:  callerPkg,
				Exported: caller.Object() != nil && caller.Object().Exported(),
			}
		}
		if _, ok := c.Funcs[calleeName]; !ok && c.isProjectPackage(calleePkg) {
			c.Funcs[calleeName] = &FuncNode{
				Name:     callee.Name(),
				FullName: calleeName,
				Package:  calleePkg,
				Exported: callee.Object() != nil && callee.Object().Exported(),
			}
		}

		return nil
	})
}

// CollectImplementsFromPackages checks which structs implement which interfaces.
func (c *Collector) CollectImplementsFromPackages(pkgs []*packages.Package) {
	var ifaces []struct {
		key  string
		typ  *types.Interface
		name string
		pkg  string
	}
	var concretes []struct {
		key  string
		typ  types.Type
		name string
		pkg  string
	}

	packages.Visit(pkgs, nil, func(pkg *packages.Package) {
		if !c.isProjectPackage(pkg.PkgPath) {
			return
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if tn, ok := obj.(*types.TypeName); ok {
				switch t := tn.Type().Underlying().(type) {
				case *types.Interface:
					if t.NumMethods() > 0 { // skip empty interfaces
						ifaces = append(ifaces, struct {
							key  string
							typ  *types.Interface
							name string
							pkg  string
						}{pkg.PkgPath + "." + name, t, name, pkg.PkgPath})
					}
				case *types.Struct:
					concretes = append(concretes, struct {
						key  string
						typ  types.Type
						name string
						pkg  string
					}{pkg.PkgPath + "." + name, tn.Type(), name, pkg.PkgPath})
				}
			}
		}
	})

	// Check implements with O(1) duplicate detection.
	seen := make(map[string]bool)
	for _, concrete := range concretes {
		for _, iface := range ifaces {
			edgeKey := concrete.key + "->" + iface.key
			if seen[edgeKey] {
				continue
			}
			// Check T implements I
			if types.Implements(concrete.typ, iface.typ) {
				c.Implements = append(c.Implements, ImplementsEdge{
					Struct:    concrete.key,
					Interface: iface.key,
				})
				seen[edgeKey] = true
				continue
			}
			// Check *T implements I
			ptr := types.NewPointer(concrete.typ)
			if types.Implements(ptr, iface.typ) {
				c.Implements = append(c.Implements, ImplementsEdge{
					Struct:    concrete.key,
					Interface: iface.key,
				})
				seen[edgeKey] = true
			}
		}
	}
}

// buildSSAFuncName derives a full name for an SSA function that matches
// the naming convention used by FuncNode.FullName.
func buildSSAFuncName(fn *ssa.Function) string {
	if fn.Pkg == nil {
		return fn.String()
	}
	pkgPath := fn.Pkg.Pkg.Path()

	// Method: (*Type).Method or Type.Method
	if recv := fn.Signature.Recv(); recv != nil {
		recvType := recv.Type()
		if ptr, ok := recvType.(*types.Pointer); ok {
			recvType = ptr.Elem()
		}
		if named, ok := recvType.(*types.Named); ok {
			return pkgPath + "." + named.Obj().Name() + "." + fn.Name()
		}
	}
	return pkgPath + "." + fn.Name()
}
