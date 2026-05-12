package model

import "time"

type Kind int

const (
	KindRepo Kind = iota
	KindDir
	KindFile
	KindSymbol
)

func (k Kind) String() string {
	switch k {
	case KindRepo:
		return "repo"
	case KindDir:
		return "dir"
	case KindFile:
		return "file"
	case KindSymbol:
		return "symbol"
	}
	return "unknown"
}

type NodeID struct {
	Kind   Kind
	Path   string
	Symbol string
}

func (n NodeID) String() string {
	if n.Symbol != "" {
		return n.Path + "::" + n.Symbol
	}
	if n.Path == "" {
		return "."
	}
	return n.Path
}

type SymbolKind int

const (
	SymFunc SymbolKind = iota
	SymMethod
	SymType
	SymVar
	SymConst
)

func (s SymbolKind) String() string {
	switch s {
	case SymFunc:
		return "func"
	case SymMethod:
		return "method"
	case SymType:
		return "type"
	case SymVar:
		return "var"
	case SymConst:
		return "const"
	}
	return ""
}

type Symbol struct {
	Name      string
	Kind      SymbolKind
	Signature string
	StartLine int
	EndLine   int
	StartByte int
	EndByte   int
	Receiver  string
}

type SymbolRef struct {
	Name string
	Path string
	Line int
}

type Metadata struct {
	Imports  []string    `json:"imports,omitempty"`
	Callers  []SymbolRef `json:"callers,omitempty"`
	Callees  []SymbolRef `json:"callees,omitempty"`
	KeyTypes []string    `json:"key_types,omitempty"`
	Gotchas  []string    `json:"gotchas,omitempty"`
	LOC      int         `json:"loc,omitempty"`
}

type Explanation struct {
	NodeID     NodeID    `json:"node_id"`
	Prose      string    `json:"prose"`
	Metadata   Metadata  `json:"metadata"`
	SourceHash string    `json:"source_hash"`
	Model      string    `json:"model"`
	PromptVer  int       `json:"prompt_ver"`
	CreatedAt  time.Time `json:"created_at"`
}
