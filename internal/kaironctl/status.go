// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/zyvorai/kairon/internal/kaironctl/style"
	"github.com/zyvorai/kairon/internal/kube"
)

type statusOpts struct {
	Namespace   string
	Wait        bool
	Timeout     time.Duration
	Interactive bool
}

func newStatusCmd(opts *Options) *cobra.Command {
	s := &statusOpts{
		Namespace:   "kairon-system",
		Timeout:     5 * time.Minute,
		Interactive: true,
	}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show Kairon control-plane status",
		Long: `Display controller Deployment, node DaemonSet, CRD presence, and Machine phase counts.

With --wait, refresh until the control plane looks ready or --timeout elapses.`,
		Example: `  $ kaironctl status
  $ kaironctl status --wait --timeout 2m`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			kc, err := kube.FromEnvironment()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			return runStatus(ctx, kc, s)
		},
	}
	cmd.Flags().StringVarP(&s.Namespace, "namespace", "n", s.Namespace, "namespace where Kairon is installed")
	cmd.Flags().BoolVar(&s.Wait, "wait", false, "wait until control plane is ready")
	cmd.Flags().DurationVar(&s.Timeout, "timeout", s.Timeout, "maximum wait duration")
	cmd.Flags().BoolVar(&s.Interactive, "interactive", true, "rewrite status in place while waiting")
	return cmd
}

type workloadStatus struct {
	Kind    string
	Name    string
	Desired int
	Ready   int
	Err     string
}

type clusterStatus struct {
	Controller workloadStatus
	Node       workloadStatus
	CRDsOK     bool
	CRDErr     string
	Phases     map[string]int
	MachineN   int
}

func runStatus(ctx context.Context, kc *kube.Client, s *statusOpts) error {
	deadline := time.Now().Add(s.Timeout)
	var lastLines int
	for {
		st, err := collectStatus(ctx, kc, s.Namespace)
		if err != nil {
			return err
		}
		var buf strings.Builder
		writeStatus(&buf, st)
		out := buf.String()
		if s.Wait && s.Interactive && lastLines > 0 && style.Enabled(os.Stdout) {
			style.ClearLines(os.Stdout, lastLines)
		}
		fmt.Fprint(os.Stdout, out)
		lastLines = strings.Count(out, "\n")
		if !s.Wait {
			return nil
		}
		if statusReady(st) {
			style.Log(style.EmojiOK, "Kairon control plane is ready")
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for Kairon to become ready")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func statusReady(st clusterStatus) bool {
	if !st.CRDsOK {
		return false
	}
	if st.Controller.Err != "" || st.Node.Err != "" {
		return false
	}
	if st.Controller.Desired == 0 || st.Controller.Ready < st.Controller.Desired {
		return false
	}
	if st.Node.Desired == 0 || st.Node.Ready < st.Node.Desired {
		return false
	}
	return true
}

func writeStatus(w io.Writer, st clusterStatus) {
	fmt.Fprintln(w, style.Wrap(w, style.Cyan+style.Bold, style.BrandMark()))
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\t%s\n", "Component", "Status")
	writeWorkload(tw, w, "Controller", st.Controller)
	writeWorkload(tw, w, "Node agent", st.Node)
	crd := style.Wrap(w, style.Green, "OK")
	if !st.CRDsOK {
		crd = style.Wrap(w, style.Red, "missing")
		if st.CRDErr != "" {
			crd += " (" + st.CRDErr + ")"
		}
	}
	fmt.Fprintf(tw, "CRDs\t%s\n", crd)
	fmt.Fprintf(tw, "Machines\t%d\n", st.MachineN)
	if len(st.Phases) > 0 {
		var parts []string
		for phase, n := range st.Phases {
			parts = append(parts, fmt.Sprintf("%s=%d", style.Phase(w, phase), n))
		}
		fmt.Fprintf(tw, "Phases\t%s\n", strings.Join(parts, ", "))
	}
	_ = tw.Flush()
}

func writeWorkload(tw *tabwriter.Writer, colorW io.Writer, label string, ws workloadStatus) {
	if ws.Err != "" {
		fmt.Fprintf(tw, "%s\t%s\n", label, style.Wrap(colorW, style.Red, ws.Err))
		return
	}
	cell := fmt.Sprintf("%s/%s  Desired: %d  Ready: %s", ws.Kind, ws.Name, ws.Desired, readyCell(colorW, ws.Ready, ws.Desired))
	fmt.Fprintf(tw, "%s\t%s\n", label, cell)
}

func readyCell(w io.Writer, ready, desired int) string {
	s := fmt.Sprintf("%d/%d", ready, desired)
	if desired > 0 && ready >= desired {
		return style.Wrap(w, style.Green, s)
	}
	if ready > 0 {
		return style.Wrap(w, style.Yellow, s)
	}
	return style.Wrap(w, style.Red, s)
}

func collectStatus(ctx context.Context, kc *kube.Client, ns string) (clusterStatus, error) {
	st := clusterStatus{Phases: map[string]int{}}
	st.Controller = getDeploymentStatus(ctx, kc, ns, "kairon-controller")
	st.Node = getDaemonSetStatus(ctx, kc, ns, "kairon-node")
	st.CRDsOK, st.CRDErr = checkMachineCRD(ctx, kc)

	machines, err := kc.ListMachines(ctx)
	if err != nil {
		// CRDs missing often surfaces here
		if st.CRDErr == "" {
			st.CRDErr = err.Error()
		}
		st.CRDsOK = false
		return st, nil
	}
	st.MachineN = len(machines)
	for _, m := range machines {
		p := m.Status.Phase
		if p == "" {
			p = "Unknown"
		}
		st.Phases[p]++
	}
	return st, nil
}

func checkMachineCRD(ctx context.Context, kc *kube.Client) (bool, string) {
	// Probe the Machines collection; 404 means CRD absent.
	_, err := kc.ListMachines(ctx)
	if err == nil {
		return true, ""
	}
	if kube.IsNotFound(err) {
		return false, "Machine CRD not found"
	}
	// Other errors (auth, connection) — treat as unknown/not ok with message.
	return false, err.Error()
}

type appsDeployment struct {
	Status struct {
		Replicas            int32 `json:"replicas"`
		ReadyReplicas       int32 `json:"readyReplicas"`
		AvailableReplicas   int32 `json:"availableReplicas"`
		UpdatedReplicas     int32 `json:"updatedReplicas"`
	} `json:"status"`
}

type appsDaemonSet struct {
	Status struct {
		DesiredNumberScheduled int32 `json:"desiredNumberScheduled"`
		NumberReady            int32 `json:"numberReady"`
		NumberAvailable        int32 `json:"numberAvailable"`
	} `json:"status"`
}

func getDeploymentStatus(ctx context.Context, kc *kube.Client, ns, name string) workloadStatus {
	ws := workloadStatus{Kind: "Deployment", Name: name}
	var dep appsDeployment
	path := fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments/%s", ns, name)
	if err := kubeGetJSON(ctx, kc, path, &dep); err != nil {
		ws.Err = err.Error()
		return ws
	}
	ws.Desired = int(dep.Status.Replicas)
	ws.Ready = int(dep.Status.ReadyReplicas)
	return ws
}

func getDaemonSetStatus(ctx context.Context, kc *kube.Client, ns, name string) workloadStatus {
	ws := workloadStatus{Kind: "DaemonSet", Name: name}
	var ds appsDaemonSet
	path := fmt.Sprintf("/apis/apps/v1/namespaces/%s/daemonsets/%s", ns, name)
	if err := kubeGetJSON(ctx, kc, path, &ds); err != nil {
		ws.Err = err.Error()
		return ws
	}
	ws.Desired = int(ds.Status.DesiredNumberScheduled)
	ws.Ready = int(ds.Status.NumberReady)
	return ws
}

// kubeGetJSON performs a GET via the client's BaseURL/Token without exporting request.
func kubeGetJSON(ctx context.Context, kc *kube.Client, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(kc.BaseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	if kc.Token != "" {
		req.Header.Set("Authorization", "Bearer "+kc.Token)
	}
	resp, err := kc.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &kube.APIError{Method: http.MethodGet, Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if out != nil && len(body) > 0 {
		return json.Unmarshal(body, out)
	}
	return nil
}
