package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

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
	fmt.Printf("NAME\tNODE\tPHASE\tCPU\tMEMORY\tIP\n")
	for _, m := range items {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", m.Metadata.Name, dash(m.Spec.NodeName), dash(m.Status.Phase), m.Spec.Resources.CPU, m.Spec.Resources.Memory, dash(m.Status.GuestIP))
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
	b, _ := json.MarshalIndent(m, "", "  ")
	fmt.Println(string(b))
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
	_ = fs.Parse(args[1:])
	if *image == "" {
		fatal(fmt.Errorf("--image PATH is required"))
	}
	m := model.Machine{TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachine}, Metadata: model.ObjectMeta{Name: name, Namespace: *ns}, Spec: model.MachineSpec{Image: model.ImageSpec{Path: *image}, Resources: model.ResourceSpec{CPU: *cpu, Memory: *memory}, Runtime: model.RuntimeSpec{Backend: *backend}, Network: model.NetworkSpec{Mode: *network, NetNS: *netns}, PowerState: "Running"}}
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
func usage()          { fmt.Fprintln(os.Stderr, "kaironctl get|describe|create|delete|start|stop|version") }
func fatal(err error) { fmt.Fprintln(os.Stderr, "error:", err); os.Exit(1) }
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
