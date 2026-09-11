package kube

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/zyvorai/kairon/internal/model"
)

func (c *Client) ListMachineDisruptionBudgets(ctx context.Context) ([]model.MachineDisruptionBudget, error) {
	var list model.MachineDisruptionBudgetList
	err := c.request(ctx, http.MethodGet, "/apis/kairon.zyvor.dev/v1alpha1/machinedisruptionbudgets", nil, &list, "")
	return list.Items, err
}

func (c *Client) PatchMachineDisruptionBudgetStatus(ctx context.Context, ns, name string, status model.MachineDisruptionBudgetStatus) error {
	path := fmt.Sprintf("/apis/kairon.zyvor.dev/v1alpha1/namespaces/%s/machinedisruptionbudgets/%s/status", url.PathEscape(ns), url.PathEscape(name))
	return c.request(ctx, http.MethodPatch, path, map[string]any{"status": status}, nil, "application/merge-patch+json")
}

func (c *Client) ListMachineMigrations(ctx context.Context) ([]model.MachineMigration, error) {
	var list model.MachineMigrationList
	err := c.request(ctx, http.MethodGet, "/apis/kairon.zyvor.dev/v1alpha1/machinemigrations", nil, &list, "")
	return list.Items, err
}

func (c *Client) PatchMachineMigrationStatus(ctx context.Context, ns, name string, status model.MachineMigrationStatus) error {
	path := fmt.Sprintf("/apis/kairon.zyvor.dev/v1alpha1/namespaces/%s/machinemigrations/%s/status", url.PathEscape(ns), url.PathEscape(name))
	return c.request(ctx, http.MethodPatch, path, map[string]any{"status": status}, nil, "application/merge-patch+json")
}
