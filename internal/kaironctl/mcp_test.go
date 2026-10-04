// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/mcp"
)

type fakeKairon struct {
	mu      sync.Mutex
	patches []string
}

func (f *fakeKairon) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/machines":
			_, _ = io.WriteString(w, `{"items":[{"metadata":{"name":"web","namespace":"default"},"spec":{"powerState":"Running","resources":{"cpu":"1","memory":"1Gi"}},"status":{"phase":"Running","nodeName":"n1","guestIP":"10.0.0.5"}}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/web":
			_, _ = io.WriteString(w, `{"metadata":{"name":"web","namespace":"default"},"spec":{"resources":{"cpu":"1","memory":"1Gi"}},"status":{"phase":"Running"}}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/kairon.zyvor.dev/v1alpha1/namespaces/default/machines/web":
			b, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.patches = append(f.patches, string(b))
			f.mu.Unlock()
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/machines/default/web/network-drops":
			if r.Header.Get("Authorization") != "Bearer ui-secret" || r.URL.Query().Get("limit") != "5" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"items":[{"reason":"dns_deny","packets":3}]}`)
		default:
			t.Logf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
}

func callMCP(t *testing.T, allowWrite bool, srvURL string, lines ...string) map[string]string {
	t.Helper()
	s := mcp.NewServer("kairon", "test", allowWrite)
	s.Add(kaironTools(&Options{Namespace: "default"}, func() (*kube.Client, error) { return kube.New(srvURL, "", "", false) })...)
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var resp struct {
			ID     int `json:"id"`
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool              `json:"isError"`
				Tools   []json.RawMessage `json:"tools"`
			} `json:"result"`
			Error *struct{ Message string } `json:"error"`
		}
		if err := json.Unmarshal([]byte(l), &resp); err != nil {
			t.Fatal(err)
		}
		key := string(rune('0' + resp.ID))
		switch {
		case resp.Error != nil:
			got[key] = "rpc-error: " + resp.Error.Message
		case resp.Result.Tools != nil:
			got[key] = l
		case len(resp.Result.Content) > 0:
			prefix := ""
			if resp.Result.IsError {
				prefix = "tool-error: "
			}
			got[key] = prefix + resp.Result.Content[0].Text
		}
	}
	return got
}

func TestMCPReadTools(t *testing.T) {
	f := &fakeKairon{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	t.Setenv("KAIRON_UI_URL", srv.URL)
	t.Setenv("KAIRON_UI_TOKEN", "ui-secret")

	got := callMCP(t, false, srv.URL,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_machines","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_machine","arguments":{"name":"web"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"machine_network","arguments":{"name":"web","kind":"network-drops","limit":5}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"machine_network","arguments":{"name":"web","kind":"bogus"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"machine_edge_identity","arguments":{"name":"web"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"get_machine","arguments":{"name":"web","extra":1}}}`,
	)
	if !strings.Contains(got["1"], `"guestIP": "10.0.0.5"`) || !strings.Contains(got["1"], `"node": "n1"`) {
		t.Fatalf("list_machines: %s", got["1"])
	}
	if !strings.Contains(got["2"], `"phase": "Running"`) {
		t.Fatalf("get_machine: %s", got["2"])
	}
	if !strings.Contains(got["3"], "dns_deny") {
		t.Fatalf("machine_network: %s", got["3"])
	}
	if !strings.HasPrefix(got["4"], "tool-error: kind must be") {
		t.Fatalf("bad kind: %s", got["4"])
	}
	if !strings.Contains(got["5"], `"identity"`) {
		t.Fatalf("identity: %s", got["5"])
	}
	if !strings.HasPrefix(got["6"], "tool-error: invalid arguments") {
		t.Fatalf("unknown field: %s", got["6"])
	}
}

func TestMCPWriteToolsAreGated(t *testing.T) {
	f := &fakeKairon{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()

	list := callMCP(t, false, srv.URL, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)["1"]
	for _, name := range []string{"set_power_state", "create_snapshot", "network_capture"} {
		if strings.Contains(list, name) {
			t.Fatalf("%s listed without --allow-write", name)
		}
	}
	denied := callMCP(t, false, srv.URL, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"set_power_state","arguments":{"name":"web","state":"Stopped"}}}`)["1"]
	if !strings.Contains(denied, "--allow-write") || len(f.patches) != 0 {
		t.Fatalf("write not gated: %s patches=%v", denied, f.patches)
	}

	got := callMCP(t, true, srv.URL,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"set_power_state","arguments":{"name":"web","state":"Stopped"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"set_power_state","arguments":{"name":"web","state":"Off"}}}`,
	)
	if !strings.Contains(got["1"], "powerState set to Stopped") || len(f.patches) != 1 || !strings.Contains(f.patches[0], `"powerState":"Stopped"`) {
		t.Fatalf("power: %s patches=%v", got["1"], f.patches)
	}
	if !strings.HasPrefix(got["2"], "tool-error: state must be") {
		t.Fatalf("bad state: %s", got["2"])
	}
}
