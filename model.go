package main

// PackageNode represents a Go package in the call graph.
type PackageNode struct {
	ImportPath string
	Name       string
	Dir        string
}

// StructNode represents a Go struct type.
type StructNode struct {
	Name       string
	Package    string
	File       string
	Line       int
	Exported   bool
	FieldCount int
}

// InterfaceNode represents a Go interface type.
type InterfaceNode struct {
	Name     string
	Package  string
	File     string
	Line     int
	Exported bool
	Methods  int
}

// FuncNode represents a Go function or method.
type FuncNode struct {
	Name     string
	FullName string // package.ReceiverType.Method or package.Func
	Package  string
	File     string
	Line     int
	Exported bool
	Receiver string // empty for standalone functions
	IsMethod bool
}

// CallEdge represents a call relationship between two functions.
type CallEdge struct {
	CallerFullName string
	CalleeFullName string
	IsDynamic      bool // dispatched via interface
	Site           string
}

// ImplementsEdge represents a struct implementing an interface.
type ImplementsEdge struct {
	Struct    string // full name of struct
	Interface string // full name of interface
}
