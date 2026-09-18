// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/cli/values"
	"helm.sh/helm/v3/pkg/engine"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/release"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/zyvorai/kairon/charts"
)

const embeddedChartSentinel = "embedded"

// helmDriver is overridden in tests (secrets | configmap | memory).
var helmDriver = "secret"

type restClientGetter struct {
	namespace string
	cfg       *rest.Config
}

func (g *restClientGetter) ToRESTConfig() (*rest.Config, error) { return g.cfg, nil }
func (g *restClientGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	c, err := discovery.NewDiscoveryClientForConfig(g.cfg)
	if err != nil {
		return nil, err
	}
	return memory.NewMemCacheClient(c), nil
}
func (g *restClientGetter) ToRESTMapper() (meta.RESTMapper, error) {
	dc, err := g.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	return restmapper.NewDeferredDiscoveryRESTMapper(dc), nil
}
func (g *restClientGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	return clientcmd.NewDefaultClientConfig(clientcmdapi.Config{}, &clientcmd.ConfigOverrides{
		Context: clientcmdapi.Context{Namespace: g.namespace},
	})
}

func restConfigFromEnv() (*rest.Config, error) {
	if base := os.Getenv("KAIRON_KUBE_URL"); base != "" {
		cfg := &rest.Config{
			Host:        base,
			BearerToken: os.Getenv("KAIRON_KUBE_TOKEN"),
			Burst:       100,
			QPS:         50,
		}
		if os.Getenv("KAIRON_KUBE_INSECURE") == "true" {
			cfg.TLSClientConfig.Insecure = true
		} else if ca := os.Getenv("KAIRON_KUBE_CA"); ca != "" {
			cfg.TLSClientConfig.CAFile = ca
		}
		return cfg, nil
	}
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return rest.InClusterConfig()
	}
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides).ClientConfig()
}

func newActionConfig(namespace string) (*action.Configuration, *cli.EnvSettings, error) {
	settings := cli.New()
	cfg, err := restConfigFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("kubernetes config: %w", err)
	}
	actionConfig := new(action.Configuration)
	getter := &restClientGetter{namespace: namespace, cfg: cfg}
	if err := actionConfig.Init(getter, namespace, helmDriver, func(format string, v ...any) {}); err != nil {
		return nil, nil, fmt.Errorf("helm action init: %w", err)
	}
	return actionConfig, settings, nil
}

// resolveChartPath returns a filesystem chart directory. Empty / "embedded"
// extracts the chart baked into the binary; otherwise uses the given path
// (or KAIRON_CHART). Caller must remove tmpDir when non-empty.
func resolveChartPath(chart string) (path string, tmpDir string, err error) {
	if chart == "" || chart == embeddedChartSentinel {
		tmpDir, err = os.MkdirTemp("", "kairon-chart-*")
		if err != nil {
			return "", "", err
		}
		path, err = charts.WriteToDir(tmpDir)
		if err != nil {
			_ = os.RemoveAll(tmpDir)
			return "", "", err
		}
		return path, tmpDir, nil
	}
	if abs, e := filepath.Abs(chart); e == nil {
		chart = abs
	}
	if _, err := os.Stat(filepath.Join(chart, "Chart.yaml")); err != nil {
		return "", "", fmt.Errorf("chart %q: %w (use --chart embedded for the baked-in chart)", chart, err)
	}
	return chart, "", nil
}

func mergeValues(sets, files []string, settings *cli.EnvSettings) (map[string]any, error) {
	providers := getter.All(settings)
	opts := &values.Options{ValueFiles: files, Values: sets}
	return opts.MergeValues(providers)
}

func sdkInstall(h *helmInstallOpts, upgradeOnly bool) (*release.Release, error) {
	chartPath, tmp, err := resolveChartPath(h.Chart)
	if err != nil {
		return nil, err
	}
	if tmp != "" {
		defer func() { _ = os.RemoveAll(tmp) }()
	}

	if h.DryRun {
		manifest, err := renderChartDryRun(chartPath, h)
		if err != nil {
			return nil, err
		}
		return &release.Release{Name: h.ReleaseName, Namespace: h.Namespace, Manifest: manifest}, nil
	}

	actionConfig, settings, err := newActionConfig(h.Namespace)
	if err != nil {
		return nil, err
	}
	vals, err := mergeValues(h.Sets, h.ValuesFiles, settings)
	if err != nil {
		return nil, fmt.Errorf("merge values: %w", err)
	}
	ch, err := loader.Load(chartPath)
	if err != nil {
		return nil, fmt.Errorf("load chart: %w", err)
	}

	if upgradeOnly {
		u := action.NewUpgrade(actionConfig)
		u.Namespace = h.Namespace
		u.Wait = h.Wait
		u.Timeout = h.Timeout
		u.Atomic = false
		if h.Version != "" {
			u.Version = h.Version
		}
		return u.Run(h.ReleaseName, ch, vals)
	}

	// upgrade --install semantics
	hist := action.NewHistory(actionConfig)
	hist.Max = 1
	_, histErr := hist.Run(h.ReleaseName)
	if histErr == nil {
		u := action.NewUpgrade(actionConfig)
		u.Namespace = h.Namespace
		u.Wait = h.Wait
		u.Timeout = h.Timeout
		if h.Version != "" {
			u.Version = h.Version
		}
		return u.Run(h.ReleaseName, ch, vals)
	}

	inst := action.NewInstall(actionConfig)
	inst.ReleaseName = h.ReleaseName
	inst.Namespace = h.Namespace
	inst.CreateNamespace = h.CreateNS
	inst.Wait = h.Wait
	inst.Timeout = h.Timeout
	if h.Version != "" {
		inst.Version = h.Version
	}
	return inst.Run(ch, vals)
}

func renderChartDryRun(chartPath string, h *helmInstallOpts) (string, error) {
	settings := cli.New()
	vals, err := mergeValues(h.Sets, h.ValuesFiles, settings)
	if err != nil {
		return "", fmt.Errorf("merge values: %w", err)
	}
	ch, err := loader.Load(chartPath)
	if err != nil {
		return "", fmt.Errorf("load chart: %w", err)
	}
	options := chartutil.ReleaseOptions{
		Name:      h.ReleaseName,
		Namespace: h.Namespace,
		IsInstall: true,
	}
	values, err := chartutil.ToRenderValues(ch, vals, options, chartutil.DefaultCapabilities)
	if err != nil {
		return "", err
	}
	files, err := engine.Render(ch, values)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for name, content := range files {
		if strings.HasSuffix(name, "NOTES.txt") || strings.TrimSpace(content) == "" {
			continue
		}
		b.WriteString("---\n# Source: ")
		b.WriteString(name)
		b.WriteString("\n")
		b.WriteString(content)
		if !strings.HasSuffix(content, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

func sdkUninstall(h *helmInstallOpts) error {
	actionConfig, _, err := newActionConfig(h.Namespace)
	if err != nil {
		return err
	}
	un := action.NewUninstall(actionConfig)
	un.Wait = h.Wait
	un.Timeout = h.Timeout
	un.DryRun = h.DryRun
	_, err = un.Run(h.ReleaseName)
	return err
}
