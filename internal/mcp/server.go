// Package mcp: stdio MCP server (JSON-RPC 2.0, newline-delimited) declaring the
// claude/channel capability so chat events are pushed into the session.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"sync"
)

const Version = "0.1.0"

// Older revisions first; 2026-07-28 is never offered (v2 client won't register channels on it).
var supportedVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

type Server struct {
	wmu      sync.Mutex
	w        *bufio.Writer
	s        *Session
	autoOnce sync.Once // auto-join the folder's chats once the client is ready
}

type request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func Run() error {
	log.SetPrefix("ccqc mcp: ")
	srv := &Server{w: bufio.NewWriter(os.Stdout)}
	srv.s = newSession(srv.channel)
	go srv.s.serveSessionPipe()
	r := bufio.NewReaderSize(os.Stdin, 1<<16)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			srv.dispatch(line)
		}
		if err != nil {
			srv.s.shutdown()
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (srv *Server) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("marshal: %v", err)
		return
	}
	srv.wmu.Lock()
	defer srv.wmu.Unlock()
	srv.w.Write(b)
	srv.w.WriteByte('\n')
	srv.w.Flush()
}

// channel pushes an event into the Claude Code session.
func (srv *Server) channel(content string, meta map[string]string) {
	srv.write(map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/claude/channel",
		"params":  map[string]any{"content": content, "meta": meta},
	})
}

func (srv *Server) dispatch(line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		srv.write(map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcError{-32700, "parse error"}})
		return
	}
	// Notifications and responses to us need no reply.
	if req.Method == "" || len(req.ID) == 0 || string(req.ID) == "null" {
		if req.Method == "notifications/initialized" {
			srv.autoOnce.Do(func() { go srv.s.autoJoin() })
		}
		return
	}
	go srv.handle(req)
}

func (srv *Server) handle(req request) {
	result, rerr := srv.route(req)
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rerr != nil {
		resp["error"] = rerr
	} else {
		resp["result"] = result
	}
	srv.write(resp)
}

func (srv *Server) route(req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		return map[string]any{
			"protocolVersion": pickVersion(p.ProtocolVersion),
			"capabilities": map[string]any{
				"tools":        map[string]any{},
				"experimental": map[string]any{"claude/channel": map[string]any{}},
			},
			"serverInfo":   map[string]any{"name": "ccqc", "version": Version},
			"instructions": instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefs}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		text, err := srv.s.Call(p.Name, p.Arguments)
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(text, false), nil
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

func pickVersion(requested string) string {
	for _, v := range supportedVersions {
		if v == requested {
			return v
		}
	}
	return supportedVersions[0]
}

func toolResult(text string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	}
}
