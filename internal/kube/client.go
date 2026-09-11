package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zyvorai/kairon/internal/model"
)

const serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func FromEnvironment() (*Client, error) {
	if base := os.Getenv("KAIRON_KUBE_URL"); base != "" {
		return New(base, os.Getenv("KAIRON_KUBE_TOKEN"), os.Getenv("KAIRON_KUBE_CA"), os.Getenv("KAIRON_KUBE_INSECURE") == "true")
	}
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, fmt.Errorf("Kubernetes endpoint not configured: set KAIRON_KUBE_URL or run in cluster")
	}
	token, err := os.ReadFile(filepath.Join(serviceAccountDir, "token"))
	if err != nil {
		return nil, fmt.Errorf("read service-account token: %w", err)
	}
	return New("https://"+host+":"+port, strings.TrimSpace(string(token)), filepath.Join(serviceAccountDir, "ca.crt"), false)
}

func New(baseURL, token, caPath string, insecure bool) (*Client, error) {
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure}} // #nosec G402 - opt-in dev mode
	if caPath != "" && !insecure {
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("read CA file %s: %w", caPath, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("invalid CA file %s", caPath)
		}
		tr.TLSClientConfig.RootCAs = pool
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTP: &http.Client{Transport: tr, Timeout: 20 * time.Second}}, nil
}

func (c *Client) request(ctx context.Context, method, path string, body any, out any, contentType string) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, r)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("kubernetes %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode Kubernetes response: %w", err)
		}
	}
	return nil
}

func (c *Client) ListMachines(ctx context.Context) ([]model.Machine, error) {
	var list model.MachineList
	err := c.request(ctx, http.MethodGet, "/apis/kairon.zyvor.dev/v1alpha1/machines", nil, &list, "")
	return list.Items, err
}

func (c *Client) ListMachinesNamespace(ctx context.Context, ns string) ([]model.Machine, error) {
	var list model.MachineList
	path := fmt.Sprintf("/apis/kairon.zyvor.dev/v1alpha1/namespaces/%s/machines", url.PathEscape(ns))
	err := c.request(ctx, http.MethodGet, path, nil, &list, "")
	return list.Items, err
}

func (c *Client) GetMachine(ctx context.Context, ns, name string) (model.Machine, error) {
	var m model.Machine
	path := fmt.Sprintf("/apis/kairon.zyvor.dev/v1alpha1/namespaces/%s/machines/%s", url.PathEscape(ns), url.PathEscape(name))
	err := c.request(ctx, http.MethodGet, path, nil, &m, "")
	return m, err
}

func (c *Client) CreateMachine(ctx context.Context, ns string, m model.Machine) (model.Machine, error) {
	var out model.Machine
	path := fmt.Sprintf("/apis/kairon.zyvor.dev/v1alpha1/namespaces/%s/machines", url.PathEscape(ns))
	err := c.request(ctx, http.MethodPost, path, m, &out, "")
	return out, err
}

func (c *Client) DeleteMachine(ctx context.Context, ns, name string) error {
	path := fmt.Sprintf("/apis/kairon.zyvor.dev/v1alpha1/namespaces/%s/machines/%s", url.PathEscape(ns), url.PathEscape(name))
	return c.request(ctx, http.MethodDelete, path, map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "propagationPolicy": "Foreground"}, nil, "")
}

func (c *Client) PatchMachine(ctx context.Context, ns, name string, patch map[string]any) error {
	path := fmt.Sprintf("/apis/kairon.zyvor.dev/v1alpha1/namespaces/%s/machines/%s", url.PathEscape(ns), url.PathEscape(name))
	return c.request(ctx, http.MethodPatch, path, patch, nil, "application/merge-patch+json")
}

func (c *Client) PatchMachineStatus(ctx context.Context, ns, name string, status model.MachineStatus) error {
	path := fmt.Sprintf("/apis/kairon.zyvor.dev/v1alpha1/namespaces/%s/machines/%s/status", url.PathEscape(ns), url.PathEscape(name))
	return c.request(ctx, http.MethodPatch, path, map[string]any{"status": status}, nil, "application/merge-patch+json")
}

func (c *Client) ListNodes(ctx context.Context) ([]model.Node, error) {
	var list model.NodeList
	err := c.request(ctx, http.MethodGet, "/api/v1/nodes", nil, &list, "")
	return list.Items, err
}

// Event is a subset of core/v1 Event used for Machine lifecycle signals.
type Event struct {
	APIVersion     string            `json:"apiVersion"`
	Kind           string            `json:"kind"`
	Metadata       model.ObjectMeta  `json:"metadata"`
	InvolvedObject map[string]string `json:"involvedObject"`
	Reason         string            `json:"reason,omitempty"`
	Message        string            `json:"message,omitempty"`
	Type           string            `json:"type,omitempty"`
	Source         map[string]string `json:"source,omitempty"`
	Count          int               `json:"count,omitempty"`
	FirstTimestamp time.Time         `json:"firstTimestamp,omitempty"`
	LastTimestamp  time.Time         `json:"lastTimestamp,omitempty"`
}

type EventList struct {
	Items []Event `json:"items"`
}

func (c *Client) CreateEvent(ctx context.Context, ns string, ev Event) error {
	if ev.APIVersion == "" {
		ev.APIVersion = "v1"
	}
	if ev.Kind == "" {
		ev.Kind = "Event"
	}
	if ev.Metadata.Namespace == "" {
		ev.Metadata.Namespace = ns
	}
	if ev.Count == 0 {
		ev.Count = 1
	}
	now := time.Now().UTC()
	if ev.FirstTimestamp.IsZero() {
		ev.FirstTimestamp = now
	}
	if ev.LastTimestamp.IsZero() {
		ev.LastTimestamp = now
	}
	path := fmt.Sprintf("/api/v1/namespaces/%s/events", url.PathEscape(ns))
	return c.request(ctx, http.MethodPost, path, ev, nil, "")
}

// Eventf records a namespaced Event referencing a Machine. Failures are ignored by callers that use `_ =`.
func (c *Client) Eventf(ctx context.Context, m model.Machine, eventType, reason, message, component string) error {
	now := time.Now().UTC()
	name := fmt.Sprintf("%s.%x", m.Metadata.Name, now.UnixNano())
	ev := Event{
		Metadata: model.ObjectMeta{Name: name, Namespace: m.Namespace()},
		InvolvedObject: map[string]string{
			"apiVersion":      model.APIVersion,
			"kind":            model.KindMachine,
			"name":            m.Metadata.Name,
			"namespace":       m.Namespace(),
			"uid":             m.Metadata.UID,
			"resourceVersion": m.Metadata.ResourceVersion,
		},
		Reason:         reason,
		Message:        message,
		Type:           eventType,
		Source:         map[string]string{"component": component},
		Count:          1,
		FirstTimestamp: now,
		LastTimestamp:  now,
	}
	return c.CreateEvent(ctx, m.Namespace(), ev)
}

func (c *Client) ListEventsForMachine(ctx context.Context, ns, name string) ([]Event, error) {
	field := fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=Machine,involvedObject.apiVersion=%s", name, model.APIVersion)
	path := fmt.Sprintf("/api/v1/namespaces/%s/events?fieldSelector=%s", url.PathEscape(ns), url.QueryEscape(field))
	var list EventList
	if err := c.request(ctx, http.MethodGet, path, nil, &list, ""); err != nil {
		return nil, err
	}
	return list.Items, nil
}
