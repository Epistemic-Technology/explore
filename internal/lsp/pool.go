package lsp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/mikethicke/explore/internal/debug"
)

// ServerConfig describes how to spawn a language server for a given language
// id (matches tsparse.Language string form). Args are passed verbatim.
type ServerConfig struct {
	Language string
	Bin      string
	Args     []string
}

// DefaultServerConfigs returns a sensible set of language server invocations.
// Callers can extend or override before passing to NewPool.
func DefaultServerConfigs() []ServerConfig {
	return []ServerConfig{
		{Language: "go", Bin: "gopls"},
		{Language: "python", Bin: "pyright-langserver", Args: []string{"--stdio"}},
		{Language: "typescript", Bin: "typescript-language-server", Args: []string{"--stdio"}},
		{Language: "tsx", Bin: "typescript-language-server", Args: []string{"--stdio"}},
		{Language: "rust", Bin: "rust-analyzer"},
		{Language: "ruby", Bin: "ruby-lsp"},
		{Language: "java", Bin: "jdtls"},
		{Language: "cpp", Bin: "clangd"},
	}
}

// Pool lazily spawns one LSP client per language. Servers that fail to start
// (missing binary, init handshake failure) are remembered as "unavailable" so
// we don't retry on every request. Pool is safe for concurrent use.
type Pool struct {
	root    string
	configs map[string]ServerConfig

	mu          sync.Mutex
	clients     map[string]*Client
	unavailable map[string]bool
	closed      bool
}

// NewPool returns a Pool that will lazy-start servers from configs against
// the given rootDir. A nil configs slice means DefaultServerConfigs().
func NewPool(rootDir string, configs []ServerConfig) *Pool {
	if configs == nil {
		configs = DefaultServerConfigs()
	}
	cm := make(map[string]ServerConfig, len(configs))
	for _, c := range configs {
		cm[c.Language] = c
	}
	return &Pool{
		root:        rootDir,
		configs:     cm,
		clients:     make(map[string]*Client),
		unavailable: make(map[string]bool),
	}
}

// ClientFor returns the running client for a language, lazy-starting it on
// first request. Returns (nil, nil) if no config is registered for the
// language or if a previous start attempt failed — callers degrade gracefully
// the same way they do for a missing single client.
//
// If a previously-started client has since died (the server process crashed
// or got killed), it's evicted and a fresh one is launched in its place. This
// keeps `u`/`d` working across the rest of a session instead of producing
// permanent broken-pipe failures.
func (p *Pool) ClientFor(ctx context.Context, language string) (*Client, error) {
	if p == nil || language == "" {
		return nil, nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errors.New("lsp: pool closed")
	}
	if c, ok := p.clients[language]; ok {
		if c.IsAlive() {
			p.mu.Unlock()
			return c, nil
		}
		// Cached client is dead — evict and fall through to (re)start. Close
		// the corpse outside the lock so we don't serialize on a process
		// kill.
		debug.Logf("lsp.pool: evicting dead client for %s", language)
		delete(p.clients, language)
		go c.Close()
	}
	if p.unavailable[language] {
		p.mu.Unlock()
		return nil, nil
	}
	cfg, ok := p.configs[language]
	if !ok {
		p.unavailable[language] = true
		p.mu.Unlock()
		return nil, nil
	}
	p.mu.Unlock()

	// Spawn outside the lock — process startup + initialize handshake can
	// take hundreds of ms; holding the mutex would serialize all callers.
	start := time.Now()
	debug.Logf("lsp.pool: starting %s for %s", cfg.Bin, language)
	c, err := Start(ctx, cfg.Bin, p.root, cfg.Args...)
	debug.Logf("lsp.pool: start %s for %s done after=%s err=%v", cfg.Bin, language, time.Since(start), err)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "lsp: %s unavailable, xref disabled for %s files: %v\n", cfg.Bin, language, err)
		p.unavailable[language] = true
		return nil, nil
	}
	// Race: another goroutine may have started the same language while we
	// were spawning. Prefer the earlier client; close ours.
	if existing, ok := p.clients[language]; ok {
		go c.Close()
		return existing, nil
	}
	p.clients[language] = c
	return c, nil
}

// Close shuts down every running client. Idempotent.
func (p *Pool) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	clients := make([]*Client, 0, len(p.clients))
	for _, c := range p.clients {
		clients = append(clients, c)
	}
	p.clients = nil
	p.mu.Unlock()
	for _, c := range clients {
		_ = c.Close()
	}
}
