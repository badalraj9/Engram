// Package mcpshim is Engram v2's MCP adapter (Q1 — IDEAS §2 approved).
//
// Transport-only stdio translator: JSON-RPC 2.0 frames on stdin/stdout
// (newline-delimited, no headers — the MCP stdio transport), speaking
// initialize/tools-list/tools-call per the 2025-06-18 wire shapes, and
// driving the 7-op core through client/go (reused, never a second
// client). No domain logic, no cache, no scheduler, no model call.
//
// Adapter rules (IDEAS §1, enforced here): stateless — every call
// carries its own source/session and dials per call state from the
// shared client; fail-closed — unreachable core returns isError, never
// invented memories. Stdout is protocol: all logging goes to stderr
// (Go's log default), and the purity test fails any non-JSON line.
//
// Modes live in cmd/engramd (--mcp shim-only against a live socket,
// --both single-process co-start). This package only serves a given
// reader/writer against a given client.
package mcpshim

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"

	goClient "engram/client/go"
)

// ProtocolVersion is the MCP wire version spoken (shapes verified
// against the 2025-06-18 tools spec at plan time).
const ProtocolVersion = "2025-06-18"

// ServerName identifies us in the initialize handshake.
const ServerName = "engram"

// serverInstructions tells the model how to use this server (MCP
// InitializeResult.instructions — some hosts gate tool availability
// on its presence; absent it, OpenCode-class clients may report the
// session blocked on instruction sources).
const serverInstructions = "Engram is truth-aware memory, not search. " +
	"remember needs structured subject/predicate/object plus source/session (no free-text field exists — structuring quality is the caller's job). " +
	"recall before acting on past context; ABSTAIN means nothing qualified — ask the user, never invent. " +
	"attest votes confirm/disconfirm on recalled ids; correct replaces wrong content via hint; " +
	"resolve settles contradiction pairs; as_of reads history."

// request is one inbound JSON-RPC frame. ID is raw: numbers and
// strings both round-trip untouched; absent ID means notification.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcErr is a JSON-RPC error body.
type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// response is one outbound frame: exactly one of Result/Err set
// (notifications produce no frame at all).
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Err     *rpcErr         `json:"error,omitempty"`
}

// Standard JSON-RPC error codes (only the three this server emits).
const (
	codeParseError   = -32700
	codeInvalidReq   = -32600
	codeMethodAbsent = -32601
	codeInvalidArgs  = -32602
)

// Server serves the tool table over a stdio pair against the core.
type Server struct {
	client *goClient.Client
	tools  []toolDef
}

// New wires a shim over a dialed client (nil client fails Serve fast —
// a shim with no core answers nothing, not even handshakes).
func New(c *goClient.Client) *Server {
	s := &Server{client: c}
	s.tools = toolTable()
	return s
}

// Serve runs the line loop until EOF or a write failure. Single thread,
// no goroutines: one frame in, at most one frame out, in order.
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	if s.client == nil {
		return fmt.Errorf("mcpshim: no core client")
	}
	out := bufio.NewWriter(w)
	defer out.Flush()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			writeFrame(out, response{JSONRPC: "2.0", Err: &rpcErr{Code: codeParseError, Message: "parse error"}})
			continue
		}
		if req.JSONRPC != "2.0" || req.Method == "" {
			writeFrame(out, response{JSONRPC: "2.0", ID: req.ID, Err: &rpcErr{Code: codeInvalidReq, Message: "invalid request"}})
			continue
		}
		if req.ID == nil {
			s.handleNotification(req)
			continue
		}
		writeFrame(out, s.dispatch(req))
	}
	return sc.Err()
}

// handleNotification sinks server-bound notifications (today only
// notifications/initialized — the handshake tail). Unknown ones log to
// stderr and drop: notifications carry no ID, so no reply is possible.
func (s *Server) handleNotification(req request) {
	if req.Method != "notifications/initialized" {
		log.Printf("mcpshim: unknown notification %q", req.Method)
	}
}

// dispatch routes one request onto exactly one response.
func (s *Server) dispatch(req request) response {
	switch req.Method {
	case "initialize":
		return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": ServerName, "version": "1.0"},
			"instructions":    serverInstructions,
		}}
	case "tools/list":
		names := make([]any, 0, len(s.tools))
		for _, t := range s.tools {
			names = append(names, map[string]any{
				"name": t.name, "description": t.description, "inputSchema": t.schema,
			})
		}
		return response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": names}}
	case "tools/call":
		return s.callTool(req)
	default:
		return response{JSONRPC: "2.0", ID: req.ID, Err: &rpcErr{Code: codeMethodAbsent, Message: "unknown method: " + req.Method}}
	}
}

// writeFrame emits one stdout line. Marshal cannot fail on the shapes
// above; a failure would be a bug, so it logs to stderr and skips
// rather than writing a torn line onto the protocol stream.
func writeFrame(w *bufio.Writer, res response) {
	b, err := json.Marshal(res)
	if err != nil {
		log.Printf("mcpshim: marshal: %v", err)
		return
	}
	b = append(b, '\n')
	if _, err := w.Write(b); err != nil {
		log.Printf("mcpshim: write: %v", err)
		return
	}
	_ = w.Flush()
}
