// Package lsp is a tiny JSON-RPC LSP client tailored to what explore needs:
// references and definition lookups against gopls. It deliberately does not
// implement the full protocol — when gopls is missing, callers degrade.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mikethicke/explore/internal/debug"
)

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	Method  string          `json:"method,omitempty"` // for server-originated notifications
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("lsp error %d: %s", e.Code, e.Message) }

// Client speaks LSP over stdio to a single language server (e.g. gopls).
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	idCounter atomic.Int32
	pendingMu sync.Mutex
	pending   map[int]chan rpcResponse

	openedMu sync.Mutex
	opened   map[string]bool

	closed atomic.Bool

	// deadMu protects dead. dead is set the first time we detect the server
	// is gone — either readLoop exits (EOF/pipe broken) or writeMessage
	// fails. Subsequent call() invocations short-circuit instead of hammering
	// a closed pipe. The Pool checks IsAlive and restarts when needed.
	deadMu sync.Mutex
	dead   error
}

// Start launches the given binary (e.g. "gopls") and performs the LSP
// initialize handshake against rootDir. Returns an error if the binary is not
// on PATH so callers can degrade gracefully. args are passed verbatim to the
// server process (e.g. ["--stdio"] for pyright).
//
// The ctx parameter governs the initialize handshake only — NOT the process
// lifetime. We deliberately use exec.Command instead of exec.CommandContext
// here: the latter SIGKILLs the process when ctx is canceled, and callers
// almost always pass a request-scoped context with `defer cancel()`. That
// turned each successful xref lookup into a "spawn gopls → use it once →
// kill it on return" cycle (EOF on stdout, broken pipe on the next call).
// Process lifetime is owned by Client.Close, which Pool calls on shutdown
// or eviction.
func Start(ctx context.Context, bin, rootDir string, args ...string) (*Client, error) {
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("%s not found on PATH: %w", bin, err)
	}
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = absRoot
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Capture stderr line-by-line into the debug log. Without this, when gopls
	// crashes or panics, the cause is invisible — and crashes do happen
	// (mid-session deaths leave the pipe broken). We drain continuously so
	// the OS buffer never fills (which would block the server's writes).
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go drainStderr(bin, stderr)

	c := &Client{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
		pending: make(map[int]chan rpcResponse),
		opened:  make(map[string]bool),
	}
	go c.readLoop()

	initParams := InitializeParams{
		ProcessID:    -1,
		RootURI:      pathToURI(absRoot),
		Capabilities: map[string]any{},
	}
	var initRes json.RawMessage
	if err := c.call(ctx, "initialize", initParams, &initRes); err != nil {
		c.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if err := c.notify("initialized", map[string]any{}); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	if c.stdin != nil {
		_ = c.notify("exit", nil)
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		return c.cmd.Process.Kill()
	}
	return nil
}

// markDead records the first failure that proved the server is gone. Idempotent:
// subsequent calls (e.g. read error followed by write error) keep the first
// reason, which is more diagnostic.
func (c *Client) markDead(reason error) {
	c.deadMu.Lock()
	defer c.deadMu.Unlock()
	if c.dead == nil {
		c.dead = reason
		debug.Logf("lsp.client: marked dead reason=%v", reason)
	}
}

// IsAlive returns true while the server is responsive. Pool uses this to
// decide whether to evict a stale client and start a fresh one.
func (c *Client) IsAlive() bool {
	if c == nil {
		return false
	}
	c.deadMu.Lock()
	defer c.deadMu.Unlock()
	return c.dead == nil
}

// deadErr returns the death reason or nil. Used inside call() to fail-fast
// instead of writing into a closed pipe and producing 50 broken-pipe lines.
func (c *Client) deadErr() error {
	c.deadMu.Lock()
	defer c.deadMu.Unlock()
	return c.dead
}

func (c *Client) nextID() int { return int(c.idCounter.Add(1)) }

func (c *Client) writeMessage(payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := c.stdin.Write([]byte(header)); err != nil {
		c.markDead(err)
		return err
	}
	if _, err := c.stdin.Write(body); err != nil {
		c.markDead(err)
		return err
	}
	return nil
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	if err := c.deadErr(); err != nil {
		// Fail fast: skip the write attempt entirely so we don't generate a
		// new broken-pipe log line for every request after the server died.
		return fmt.Errorf("lsp server unavailable: %w", err)
	}
	id := c.nextID()
	ch := make(chan rpcResponse, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	start := time.Now()
	debug.Logf("lsp.call: send method=%s id=%d", method, id)
	if err := c.writeMessage(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		debug.Logf("lsp.call: write err method=%s id=%d err=%v", method, id, err)
		return err
	}
	select {
	case <-ctx.Done():
		debug.Logf("lsp.call: ctx done method=%s id=%d after=%s err=%v", method, id, time.Since(start), ctx.Err())
		return ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			debug.Logf("lsp.call: rpc err method=%s id=%d after=%s err=%v", method, id, time.Since(start), resp.Error)
			return resp.Error
		}
		debug.Logf("lsp.call: ok method=%s id=%d after=%s resultLen=%d", method, id, time.Since(start), len(resp.Result))
		if out == nil || len(resp.Result) == 0 {
			return nil
		}
		return json.Unmarshal(resp.Result, out)
	}
}

func (c *Client) notify(method string, params any) error {
	return c.writeMessage(rpcNotification{JSONRPC: "2.0", Method: method, Params: params})
}

func (c *Client) readLoop() {
	for {
		msg, err := readMessage(c.stdout)
		if err != nil {
			// readMessage returns io.EOF when the server's stdout closes
			// (usually because the process exited). Mark dead so subsequent
			// call()s fail-fast and waiters get released.
			c.markDead(fmt.Errorf("server stdout closed: %w", err))
			c.releasePending(err)
			return
		}
		var resp rpcResponse
		if err := json.Unmarshal(msg, &resp); err != nil {
			continue
		}
		if resp.ID == 0 {
			// notification from server; we ignore window/log/showMessage etc.
			continue
		}
		c.pendingMu.Lock()
		ch, ok := c.pending[resp.ID]
		c.pendingMu.Unlock()
		if ok {
			ch <- resp
		}
	}
}

// drainStderr scans the language server's stderr line-by-line into the debug
// log. Lines are tagged with the binary name so multiple servers (gopls,
// pyright, clangd, …) are distinguishable. The bufio.Scanner default buffer
// is 64KB; gopls panic stack traces can exceed that, so we bump to 1MB and
// log a marker if anything still goes over.
func drainStderr(bin string, r io.ReadCloser) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		debug.Logf("lsp.stderr[%s]: %s", bin, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		debug.Logf("lsp.stderr[%s]: scan err=%v", bin, err)
	}
}

// releasePending pushes a synthetic error response to every pending caller
// so they don't block forever waiting on ctx.Done(). Called once when the
// read loop exits.
func (c *Client) releasePending(reason error) {
	c.pendingMu.Lock()
	chans := make([]chan rpcResponse, 0, len(c.pending))
	for _, ch := range c.pending {
		chans = append(chans, ch)
	}
	c.pendingMu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- rpcResponse{Error: &rpcError{Code: -32099, Message: "server exited: " + reason.Error()}}:
		default:
		}
	}
}

func readMessage(r *bufio.Reader) ([]byte, error) {
	var length int
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, err
			}
			length = n
		}
	}
	if length <= 0 {
		return nil, errors.New("missing content length")
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// EnsureOpen sends a didOpen for the file if we haven't already.
func (c *Client) EnsureOpen(ctx context.Context, path, languageID string, text []byte) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	c.openedMu.Lock()
	if c.opened[abs] {
		c.openedMu.Unlock()
		return nil
	}
	c.opened[abs] = true
	c.openedMu.Unlock()

	return c.notify("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{
			URI:        pathToURI(abs),
			LanguageID: languageID,
			Version:    1,
			Text:       string(text),
		},
	})
}

// References returns reference locations at the given position. Line/char are 0-based.
func (c *Client) References(ctx context.Context, path string, line, character int, includeDecl bool) ([]Location, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	var locs []Location
	err = c.call(ctx, "textDocument/references", ReferenceParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(abs)},
		Position:     Position{Line: line, Character: character},
		Context:      ReferenceContext{IncludeDeclaration: includeDecl},
	}, &locs)
	return locs, err
}

// Definition returns definition locations at the given position.
func (c *Client) Definition(ctx context.Context, path string, line, character int) ([]Location, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// gopls returns either Location or []Location; ask for an array via the array variant.
	var raw json.RawMessage
	err = c.call(ctx, "textDocument/definition", DefinitionParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(abs)},
		Position:     Position{Line: line, Character: character},
	}, &raw)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var arr []Location
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr, nil
	}
	var single Location
	if err := json.Unmarshal(raw, &single); err == nil {
		return []Location{single}, nil
	}
	return nil, nil
}

func pathToURI(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String()
}

// URIToPath converts a file:// URI back to a filesystem path.
func URIToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return uri
	}
	return filepath.FromSlash(u.Path)
}
