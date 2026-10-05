// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package mcp is a minimal Model Context Protocol server over stdio:
// newline-delimited JSON-RPC 2.0 with the tools capability only.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync"
)

// LatestProtocolVersion is answered when the client asks for a version
// this server does not know.
const LatestProtocolVersion = "2025-06-18"

var supportedVersions = map[string]bool{
	"2024-11-05":          true,
	"2025-03-26":          true,
	LatestProtocolVersion: true,
}

// MaxOutput caps a tool result so one call cannot flood the model's context.
const MaxOutput = 64 << 10

// Tool is one callable tool. Write tools change state and are only
// listed and callable when the server allows writes.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Write       bool
	Call        func(ctx context.Context, args json.RawMessage) (string, error)
}

// Server dispatches JSON-RPC requests to registered tools.
type Server struct {
	Name       string
	Version    string
	AllowWrite bool
	// Audit, when set, is called before every Write tool runs (outcome
	// "intent") and after it returns ("ok" or the error text). An error
	// on the intent call refuses the write.
	Audit func(ctx context.Context, tool string, args json.RawMessage, outcome string) error

	tools map[string]Tool
}

// NewServer returns a server with no tools.
func NewServer(name, version string, allowWrite bool) *Server {
	return &Server{Name: name, Version: version, AllowWrite: allowWrite, tools: map[string]Tool{}}
}

// Add registers tools; a later tool with the same name replaces an earlier one.
func (s *Server) Add(tools ...Tool) {
	for _, t := range tools {
		s.tools[t.Name] = t
	}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Serve reads requests from r and writes responses to w until r ends or
// ctx is cancelled. Requests run concurrently; responses are written
// whole, one per line.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	var mu sync.Mutex
	var wg sync.WaitGroup
	write := func(resp response) {
		b, err := json.Marshal(resp)
		if err != nil {
			b, _ = json.Marshal(response{JSONRPC: "2.0", ID: resp.ID, Error: &rpcError{Code: -32603, Message: err.Error()}})
		}
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write(append(b, '\n'))
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			write(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: codeParse, Message: "parse error: " + err.Error()}})
			continue
		}
		if req.Method == "" {
			if len(req.ID) > 0 {
				write(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: codeInvalidRequest, Message: "missing method"}})
			}
			continue
		}
		if len(req.ID) == 0 {
			continue // notification; nothing to answer
		}
		wg.Add(1)
		go func(req request) {
			defer wg.Done()
			result, rerr := s.handle(ctx, req)
			resp := response{JSONRPC: "2.0", ID: req.ID}
			if rerr != nil {
				resp.Error = rerr
			} else {
				resp.Result = result
			}
			write(resp)
		}(req)
	}
	wg.Wait()
	if err := sc.Err(); err != nil {
		return err
	}
	return ctx.Err()
}

func (s *Server) handle(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := LatestProtocolVersion
		if supportedVersions[p.ProtocolVersion] {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.listed()}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call needs a name"}
		}
		t, ok := s.tools[p.Name]
		if !ok || (t.Write && !s.AllowWrite) {
			if ok {
				return toolResult(fmt.Sprintf("%s changes state; start the server with --allow-write to use it", p.Name), true), nil
			}
			return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool " + p.Name}
		}
		args := p.Arguments
		if len(args) == 0 || string(args) == "null" {
			args = json.RawMessage("{}")
		}
		if t.Write && s.Audit != nil {
			if err := s.Audit(ctx, t.Name, args, "intent"); err != nil {
				return toolResult("audit log unavailable; write refused: "+err.Error(), true), nil
			}
		}
		out, err := t.Call(ctx, args)
		if t.Write && s.Audit != nil {
			outcome := "ok"
			if err != nil {
				outcome = err.Error()
			}
			_ = s.Audit(ctx, t.Name, args, outcome)
		}
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(out, false), nil
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}
	}
}

func (s *Server) listed() []map[string]any {
	names := make([]string, 0, len(s.tools))
	for name, t := range s.tools {
		if t.Write && !s.AllowWrite {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		t := s.tools[name]
		schema := t.Schema
		if schema == nil {
			schema = Object(nil)
		}
		entry := map[string]any{"name": t.Name, "description": t.Description, "inputSchema": schema}
		if !t.Write {
			entry["annotations"] = map[string]any{"readOnlyHint": true}
		}
		out = append(out, entry)
	}
	return out
}

func toolResult(text string, isError bool) map[string]any {
	if len(text) > MaxOutput {
		text = text[:MaxOutput] + fmt.Sprintf("\n[truncated: output exceeded %d bytes; narrow the request, e.g. with limit]", MaxOutput)
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}

// Object builds a JSON Schema object from property schemas; required
// names are listed after the properties.
func Object(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// String is a string property; enum values, if any, restrict it.
func String(desc string, enum ...string) map[string]any {
	s := map[string]any{"type": "string", "description": desc}
	if len(enum) > 0 {
		s["enum"] = enum
	}
	return s
}

// Integer is an integer property bounded by min and max.
func Integer(desc string, minimum, maximum int) map[string]any {
	return map[string]any{"type": "integer", "description": desc, "minimum": minimum, "maximum": maximum}
}

// JSON renders v indented for a tool result.
func JSON(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
