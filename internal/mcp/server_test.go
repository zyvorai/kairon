// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testServer(allowWrite bool) *Server {
	s := NewServer("test", "v0", allowWrite)
	s.Add(
		Tool{Name: "echo", Description: "echo", Schema: Object(map[string]any{"msg": String("m")}, "msg"),
			Call: func(_ context.Context, args json.RawMessage) (string, error) {
				var a struct{ Msg string }
				if err := json.Unmarshal(args, &a); err != nil {
					return "", err
				}
				return a.Msg, nil
			}},
		Tool{Name: "fail", Call: func(context.Context, json.RawMessage) (string, error) { return "", errors.New("boom") }},
		Tool{Name: "big", Call: func(context.Context, json.RawMessage) (string, error) {
			return strings.Repeat("x", MaxOutput+10), nil
		}},
		Tool{Name: "stop", Write: true, Call: func(context.Context, json.RawMessage) (string, error) { return "stopped", nil }},
	)
	return s
}

// run sends lines to the server and returns responses keyed by id.
func run(t *testing.T, s *Server, lines ...string) map[string]map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]any{}
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad response %q: %v", l, err)
		}
		id, _ := json.Marshal(m["id"])
		got[string(id)] = m
	}
	return got
}

func resultText(t *testing.T, resp map[string]any) (string, bool) {
	t.Helper()
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", resp)
	}
	content := res["content"].([]any)[0].(map[string]any)
	return content["text"].(string), res["isError"].(bool)
}

func TestInitializeNegotiatesVersion(t *testing.T) {
	got := run(t, testServer(false),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"ping"}`,
	)
	if len(got) != 3 {
		t.Fatalf("notification must not be answered: %v", got)
	}
	if v := got["1"]["result"].(map[string]any)["protocolVersion"]; v != "2024-11-05" {
		t.Fatalf("version %v", v)
	}
	if v := got["2"]["result"].(map[string]any)["protocolVersion"]; v != LatestProtocolVersion {
		t.Fatalf("fallback version %v", v)
	}
	if got["3"]["error"] != nil {
		t.Fatalf("ping: %v", got["3"])
	}
}

func TestToolsListHidesWriteTools(t *testing.T) {
	names := func(allow bool) []string {
		got := run(t, testServer(allow), `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		tools := got["1"]["result"].(map[string]any)["tools"].([]any)
		out := make([]string, 0, len(tools))
		for _, tl := range tools {
			out = append(out, tl.(map[string]any)["name"].(string))
		}
		return out
	}
	if n := strings.Join(names(false), ","); n != "big,echo,fail" {
		t.Fatalf("read-only list %s", n)
	}
	if n := strings.Join(names(true), ","); n != "big,echo,fail,stop" {
		t.Fatalf("write list %s", n)
	}
}

func TestToolsCall(t *testing.T) {
	got := run(t, testServer(false),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"msg":"hi"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"fail"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"stop"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"big"}}`,
	)
	if txt, isErr := resultText(t, got["1"]); txt != "hi" || isErr {
		t.Fatalf("echo %q %v", txt, isErr)
	}
	if txt, isErr := resultText(t, got["2"]); txt != "boom" || !isErr {
		t.Fatalf("fail %q %v", txt, isErr)
	}
	if txt, isErr := resultText(t, got["3"]); !isErr || !strings.Contains(txt, "--allow-write") {
		t.Fatalf("write gate %q %v", txt, isErr)
	}
	if got["4"]["error"] == nil {
		t.Fatalf("unknown tool should be a protocol error: %v", got["4"])
	}
	if txt, _ := resultText(t, got["5"]); !strings.Contains(txt, "[truncated") || len(txt) > MaxOutput+200 {
		t.Fatalf("big output not truncated: %d", len(txt))
	}
}

func TestProtocolErrors(t *testing.T) {
	got := run(t, testServer(false),
		`not json`,
		`{"jsonrpc":"2.0","id":7,"method":"resources/list"}`,
	)
	if e := got["null"]["error"].(map[string]any); e["code"].(float64) != codeParse {
		t.Fatalf("parse error %v", e)
	}
	if e := got["7"]["error"].(map[string]any); e["code"].(float64) != codeMethodNotFound {
		t.Fatalf("method error %v", e)
	}
}

func TestAuditWrapsWriteToolsAndRefusesWhenUnavailable(t *testing.T) {
	s := testServer(true)
	var calls []string
	s.Audit = func(_ context.Context, tool string, _ json.RawMessage, outcome string) error {
		calls = append(calls, tool+":"+outcome)
		return nil
	}
	got := run(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"stop"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"msg":"hi"}}}`,
	)
	if txt, isErr := resultText(t, got["1"]); txt != "stopped" || isErr {
		t.Fatalf("stop %q %v", txt, isErr)
	}
	if strings.Join(calls, ",") != "stop:intent,stop:ok" {
		t.Fatalf("audit calls = %v; read tools must not be audited", calls)
	}

	ran := false
	s = NewServer("test", "v0", true)
	s.Add(Tool{Name: "stop", Write: true, Call: func(context.Context, json.RawMessage) (string, error) { ran = true; return "", nil }})
	s.Audit = func(context.Context, string, json.RawMessage, string) error { return errors.New("disk full") }
	got = run(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"stop"}}`)
	if txt, isErr := resultText(t, got["1"]); !isErr || !strings.Contains(txt, "write refused") || ran {
		t.Fatalf("refusal %q %v ran=%v", txt, isErr, ran)
	}
}
