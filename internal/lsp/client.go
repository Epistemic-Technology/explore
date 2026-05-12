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
}

// Start launches the given binary (e.g. "gopls") and performs the LSP
// initialize handshake against rootDir. Returns an error if the binary is not
// on PATH so callers can degrade gracefully.
func Start(ctx context.Context, bin, rootDir string) (*Client, error) {
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("%s not found on PATH: %w", bin, err)
	}
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin)
	cmd.Dir = absRoot
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Discard stderr to avoid blocking on a full pipe; users debug with EXPLORE_LSP_LOG.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}

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
	_ = c.notify("exit", nil)
	_ = c.stdin.Close()
	return c.cmd.Process.Kill()
}

func (c *Client) nextID() int { return int(c.idCounter.Add(1)) }

func (c *Client) writeMessage(payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := c.stdin.Write([]byte(header)); err != nil {
		return err
	}
	_, err = c.stdin.Write(body)
	return err
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
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

	if err := c.writeMessage(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
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
