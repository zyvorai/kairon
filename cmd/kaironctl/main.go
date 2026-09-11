package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	kc, err := kube.FromEnvironment()
	if err != nil {
		fatal(err)
	}
	switch os.Args[1] {
	case "get":
		cmdGet(ctx, kc, os.Args[2:])
	case "describe":
		cmdDescribe(ctx, kc, os.Args[2:])
	case "create":
		cmdCreate(ctx, kc, os.Args[2:])
	case "delete":
		cmdDelete(ctx, kc, os.Args[2:])
	case "start":
		cmdPower(ctx, kc, os.Args[2:], "Running")
	case "stop":
		cmdPower(ctx, kc, os.Args[2:], "Stopped")
	case "console":
		cmdConsole(ctx, kc, os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
}

func nsFlag(name string, args []string) (string, []string) {
	ns := "default"
	var out []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "-n" || args[i] == "--namespace") && i+1 < len(args) {
			ns = args[i+1]
			i++
			continue
		}
		out = append(out, args[i])
	}
	return ns, out
}

func cmdGet(ctx context.Context, kc *kube.Client, args []string) {
	ns, args := nsFlag("get", args)
	_ = args
	items, err := kc.ListMachinesNamespace(ctx, ns)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("NAME\tNODE\tPHASE\tREADY\tCPU\tMEMORY\tIP\n")
	for _, m := range items {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\t%s\n", m.Metadata.Name, dash(m.Spec.NodeName), dash(m.Status.Phase), dash(model.ConditionStatus(m.Status.Conditions, model.ConditionReady)), m.Spec.Resources.CPU, m.Spec.Resources.Memory, dash(m.Status.GuestIP))
	}
}

func cmdDescribe(ctx context.Context, kc *kube.Client, args []string) {
	ns, args := nsFlag("describe", args)
	if len(args) != 1 {
		fatal(fmt.Errorf("describe requires NAME"))
	}
	m, err := kc.GetMachine(ctx, ns, args[0])
	if err != nil {
		fatal(err)
	}
	fmt.Printf("Name:         %s\n", m.Metadata.Name)
	fmt.Printf("Namespace:    %s\n", m.Namespace())
	fmt.Printf("Generation:   %d\n", m.Metadata.Generation)
	fmt.Printf("Node:         %s\n", dash(m.Spec.NodeName))
	fmt.Printf("Phase:        %s\n", dash(m.Status.Phase))
	fmt.Printf("ObservedGen:  %d\n", m.Status.ObservedGeneration)
	fmt.Printf("RuntimeID:    %s\n", dash(m.Status.RuntimeID))
	fmt.Printf("GuestIP:      %s\n", dash(m.Status.GuestIP))
	fmt.Printf("Image:        %s\n", m.Spec.Image.Path)
	if m.Spec.Image.Digest != "" {
		fmt.Printf("Digest:       %s\n", m.Spec.Image.Digest)
	}
	fmt.Printf("Resources:    cpu=%s memory=%s\n", m.Spec.Resources.CPU, m.Spec.Resources.Memory)
	fmt.Printf("PowerState:   %s\n", m.DesiredPowerState())
	if m.Spec.CloudInit.UserData != "" || len(m.Spec.CloudInit.SSHPublicKeys) > 0 {
		fmt.Printf("CloudInit:    userData=%t sshKeys=%d\n", m.Spec.CloudInit.UserData != "", len(m.Spec.CloudInit.SSHPublicKeys))
	}
	if m.Status.Message != "" {
		fmt.Printf("Message:      %s\n", m.Status.Message)
	}
	fmt.Println("Conditions:")
	if len(m.Status.Conditions) == 0 {
		fmt.Println("  <none>")
	}
	for _, c := range m.Status.Conditions {
		fmt.Printf("  %s=%s reason=%s message=%s transition=%s\n", c.Type, c.Status, c.Reason, c.Message, c.LastTransitionTime.UTC().Format(time.RFC3339))
	}
	events, err := kc.ListEventsForMachine(ctx, ns, m.Metadata.Name)
	fmt.Println("Events:")
	if err != nil {
		fmt.Printf("  <unavailable: %v>\n", err)
		return
	}
	if len(events) == 0 {
		fmt.Println("  <none>")
	}
	for _, ev := range events {
		fmt.Printf("  %s %s %s\n", ev.Type, ev.Reason, ev.Message)
	}
}

func cmdCreate(ctx context.Context, kc *kube.Client, args []string) {
	if len(args) < 1 {
		fatal(fmt.Errorf("usage: kaironctl create NAME --image PATH [flags]"))
	}
	name := args[0]
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	ns := fs.String("namespace", "default", "namespace")
	image := fs.String("image", "", "FluxVM host-local image path")
	cpu := fs.String("cpu", "2", "vCPU quantity")
	memory := fs.String("memory", "2Gi", "memory quantity")
	backend := fs.String("backend", "qemu", "qemu|cloud-hypervisor|firecracker|flux-vm|auto")
	network := fs.String("network", "user", "user|tap|macvtap")
	netns := fs.Bool("netns", false, "use per-VM network namespace for TAP")
	sshKey := fs.String("ssh-key", "", "optional SSH public key for cloud-init")
	_ = fs.Parse(args[1:])
	if *image == "" {
		fatal(fmt.Errorf("--image PATH is required"))
	}
	spec := model.MachineSpec{
		Image:      model.ImageSpec{Path: *image},
		Resources:  model.ResourceSpec{CPU: *cpu, Memory: *memory},
		Runtime:    model.RuntimeSpec{Backend: *backend},
		Network:    model.NetworkSpec{Mode: *network, NetNS: *netns},
		PowerState: "Running",
	}
	if *sshKey != "" {
		spec.CloudInit.SSHPublicKeys = []string{*sshKey}
	}
	m := model.Machine{TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachine}, Metadata: model.ObjectMeta{Name: name, Namespace: *ns}, Spec: spec}
	out, err := kc.CreateMachine(ctx, *ns, m)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("machine/%s created\n", out.Metadata.Name)
}

func cmdDelete(ctx context.Context, kc *kube.Client, args []string) {
	ns, args := nsFlag("delete", args)
	if len(args) != 1 {
		fatal(fmt.Errorf("delete requires NAME"))
	}
	if err := kc.DeleteMachine(ctx, ns, args[0]); err != nil {
		fatal(err)
	}
	fmt.Printf("machine/%s deleted\n", args[0])
}

func cmdPower(ctx context.Context, kc *kube.Client, args []string, state string) {
	ns, args := nsFlag(strings.ToLower(state), args)
	if len(args) != 1 {
		fatal(fmt.Errorf("command requires NAME"))
	}
	if err := kc.PatchMachine(ctx, ns, args[0], map[string]any{"spec": map[string]any{"powerState": state}}); err != nil {
		fatal(err)
	}
	fmt.Printf("machine/%s -> %s\n", args[0], state)
}

func cmdConsole(ctx context.Context, kc *kube.Client, args []string) {
	ns, args := nsFlag("console", args)
	if len(args) != 1 {
		fatal(fmt.Errorf("console requires NAME"))
	}
	fluxURL := os.Getenv("KAIRON_FLUXVM_URL")
	if fluxURL == "" {
		fatal(fmt.Errorf("set KAIRON_FLUXVM_URL to the node-local FluxVM API (e.g. http://127.0.0.1:7788)"))
	}
	m, err := kc.GetMachine(ctx, ns, args[0])
	if err != nil {
		fatal(err)
	}
	if m.Status.RuntimeID == "" {
		fatal(fmt.Errorf("machine %s has no runtimeID yet", args[0]))
	}
	fc := fluxvm.New(fluxURL, os.Getenv("KAIRON_FLUXVM_TOKEN"))
	info, err := fc.Console(ctx, m.Status.RuntimeID)
	if err != nil {
		fatal(fmt.Errorf("FluxVM console unavailable: %w", err))
	}
	b, _ := json.MarshalIndent(info, "", "  ")
	fmt.Println(string(b))
}

func usage() {
	fmt.Fprintln(os.Stderr, "kaironctl get|describe|create|delete|start|stop|console|version")
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "error:", err); os.Exit(1) }
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
