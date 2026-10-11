// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/zyvorai/kairon/internal/kube"
)

type connectivityOpts struct {
	Namespace string
	Timeout   time.Duration
	Output    string
}

func newConnectivityCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "connectivity",
		Short: "Verify that Kairon's components are reachable",
	}
	o := &connectivityOpts{Namespace: "kairon-system", Timeout: 30 * time.Second}
	test := &cobra.Command{
		Use:   "test",
		Short: "Probe every Kairon pod's /readyz and /healthz through the API server",
		Long: `For each controller, node agent and dashboard pod (and the dashboard Service) request
/healthz and /readyz through the Kubernetes API server's proxy, so the check needs no
port-forward and no cluster-internal address. It is read-only and non-disruptive.

It answers "can the API server reach each component and is it ready", which is the
usual first question; it does not create Machines or exercise live migration.`,
		Example: `  $ kaironctl connectivity test
  $ kaironctl connectivity test -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			kc, err := newKubeClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			ctx, cancel := context.WithTimeout(ctx, o.Timeout)
			defer cancel()
			rep := runConnectivity(ctx, kc, o.Namespace)
			out := cmd.OutOrStdout()
			if o.Output != "" {
				if err := writeStructured(out, rep, o.Output); err != nil {
					return err
				}
			} else {
				writeDoctor(out, rep)
			}
			if rep.Fail > 0 {
				return fmt.Errorf("connectivity test: %d probe(s) failed", rep.Fail)
			}
			return nil
		},
	}
	test.Flags().StringVarP(&o.Namespace, "namespace", "n", o.Namespace, "namespace where Kairon is installed")
	test.Flags().DurationVar(&o.Timeout, "timeout", o.Timeout, "overall time limit")
	test.Flags().StringVarP(&o.Output, "output", "o", "", "output format: json|yaml (default: table)")
	root.AddCommand(test)
	return root
}

func runConnectivity(ctx context.Context, kc *kube.Client, ns string) doctorReport {
	rep := doctorReport{}
	probed := 0
	for _, c := range []struct{ label, component string }{
		{"controller", "kairon-controller"},
		{"node agent", "kairon-node"},
		{"dashboard", "kairon-ui"},
	} {
		pods, err := listPods(ctx, kc, ns, "app.kubernetes.io/name="+c.component)
		if err != nil {
			rep.add(doctorCheck{Name: c.label, Status: checkFail, Detail: err.Error(), Hint: "check -n/--namespace and RBAC"})
			continue
		}
		if len(pods) == 0 {
			rep.add(doctorCheck{Name: c.label, Status: checkSkip, Detail: "no pods"})
			continue
		}
		for _, p := range pods {
			name := fmt.Sprintf("%s %s", c.label, p.Name)
			if p.HealthPort == 0 {
				rep.add(doctorCheck{Name: name, Status: checkSkip, Detail: "pod exposes no health port"})
				continue
			}
			target := fmt.Sprintf("%s:%d", p.Name, p.HealthPort)
			failed := false
			for _, probe := range []string{"/healthz", "/readyz"} {
				probed++
				if _, err := proxyGet(ctx, kc, "pods", ns, target, probe); err != nil {
					rep.add(doctorCheck{Name: name, Status: checkFail, Detail: probe + ": " + err.Error(),
						Hint: "kaironctl logs " + c.component[len("kairon-"):] + "; kaironctl events --warnings"})
					failed = true
					break
				}
			}
			if !failed {
				rep.add(doctorCheck{Name: name, Status: checkOK, Detail: fmt.Sprintf("/healthz and /readyz on :%d", p.HealthPort)})
			}
		}
	}
	// The dashboard Service port, as clients reach it.
	if _, err := proxyGet(ctx, kc, "services", ns, "http:kairon-ui:18082", "/readyz"); err == nil {
		rep.add(doctorCheck{Name: "dashboard service", Status: checkOK, Detail: "kairon-ui:18082 /readyz"})
	} else if !kube.IsNotFound(err) {
		rep.add(doctorCheck{Name: "dashboard service", Status: checkWarn, Detail: err.Error(),
			Hint: "the Service may use a different port (ui.service.port) or the dashboard is disabled"})
	}
	rep.Healthy = rep.Fail == 0 && probed > 0
	return rep
}
