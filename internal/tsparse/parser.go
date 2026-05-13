// Package tsparse extracts top-level symbols from source files using tree-sitter.
//
// Per-language extractors live in sibling files (go.go, python.go, etc.) and
// are dispatched from Parse(). Adding a new language means: (1) add a Lang
// constant, (2) wire DetectLanguage, (3) add parseFoo(), (4) wire the Parse
// dispatch switch, (5) add tests.
package tsparse

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/mikethicke/explore/internal/model"
)

type Language string

const (
	LangGo         Language = "go"
	LangPython     Language = "python"
	LangTypeScript Language = "typescript"
	LangTSX        Language = "tsx"
	LangRust       Language = "rust"
	LangRuby       Language = "ruby"
	LangJava       Language = "java"
	LangCPP        Language = "cpp"
	LangUnknown    Language = ""
)

// LSPLanguageID returns the standard LSP language identifier for a language,
// used in textDocument/didOpen. Empty for LangUnknown.
func (l Language) LSPLanguageID() string {
	switch l {
	case LangGo:
		return "go"
	case LangPython:
		return "python"
	case LangTypeScript:
		return "typescript"
	case LangTSX:
		return "typescriptreact"
	case LangRust:
		return "rust"
	case LangRuby:
		return "ruby"
	case LangJava:
		return "java"
	case LangCPP:
		return "cpp"
	}
	return ""
}

func DetectLanguage(path string) Language {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return LangGo
	case ".py":
		return LangPython
	case ".ts":
		return LangTypeScript
	case ".tsx":
		return LangTSX
	case ".rs":
		return LangRust
	case ".rb":
		return LangRuby
	case ".java":
		return LangJava
	case ".cc", ".cpp", ".cxx", ".c++", ".hpp", ".hh", ".hxx", ".h++", ".h":
		return LangCPP
	}
	return LangUnknown
}

type ParsedFile struct {
	Path    string
	Lang    Language
	Imports []string
	Symbols []model.Symbol
}

// Parse extracts imports and top-level symbols from a source file.
func Parse(ctx context.Context, path string, src []byte) (*ParsedFile, error) {
	lang := DetectLanguage(path)
	switch lang {
	case LangGo:
		return parseGo(ctx, path, src)
	case LangPython:
		return parsePython(ctx, path, src)
	case LangTypeScript:
		return parseTypeScript(ctx, path, src, false)
	case LangTSX:
		return parseTypeScript(ctx, path, src, true)
	case LangRust:
		return parseRust(ctx, path, src)
	case LangRuby:
		return parseRuby(ctx, path, src)
	case LangJava:
		return parseJava(ctx, path, src)
	case LangCPP:
		return parseCPP(ctx, path, src)
	}
	return &ParsedFile{Path: path, Lang: LangUnknown}, nil
}

// SymbolSource returns the byte slice for a symbol from the parsed file's source.
func SymbolSource(src []byte, s model.Symbol) []byte {
	if s.StartByte < 0 || s.EndByte > len(src) || s.StartByte >= s.EndByte {
		return nil
	}
	return src[s.StartByte:s.EndByte]
}
