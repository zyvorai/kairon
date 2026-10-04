// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/zyvorai/kairon/internal/fluxvm"
	"github.com/zyvorai/kairon/internal/kube"
	"github.com/zyvorai/kairon/internal/model"
)

type fakeDeviceFlux struct {
	mu    sync.Mutex
	disks map[string]string
	nics  map[string]string
	calls []string
}

func (f *fakeDeviceFlux) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]string
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/vms/vm-1/disks":
		items := []map[string]string{{"name": "root", "path": "/w/root.qcow2"}}
		for n, p := range f.disks {
			items = append(items, map[string]string{"name": n, "path": p})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-1/disks":
		f.disks[body["name"]] = body["path"]
		f.calls = append(f.calls, "attach "+body["name"]+" "+body["path"])
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("{}"))
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1/vms/vm-1/disks/"):
		name := strings.TrimPrefix(r.URL.Path, "/v1/vms/vm-1/disks/")
		delete(f.disks, name)
		f.calls = append(f.calls, "detach "+name)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-1/hotplug/nic":
		f.nics[body["mac"]] = body["bridge"]
		f.calls = append(f.calls, "nic+ "+body["bridge"]+" "+body["mac"])
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/vms/vm-1/hotplug/nic/unplug":
		delete(f.nics, body["mac"])
		f.calls = append(f.calls, "nic- "+body["mac"])
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func (f *fakeDeviceFlux) record() *fluxvm.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := &fluxvm.Record{IDValue: "vm-1", Status: "running"}
	for mac, br := range f.nics {
		rec.Request.Network.Extra = append(rec.Request.Network.Extra, struct {
			Bridge  string `json:"bridge,omitempty"`
			MAC     string `json:"mac,omitempty"`
			TapName string `json:"tap_name,omitempty"`
		}{Bridge: br, MAC: mac, TapName: "hn" + mac[len(mac)-2:]})
	}
	return rec
}

func newDeviceAgent(t *testing.T, f *fakeDeviceFlux, pvs map[string]model.PersistentVolume) *Agent {
	t.Helper()
	fs := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(fs.Close)
	ks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const pvcPrefix = "/api/v1/namespaces/prod/persistentvolumeclaims/"
		const pvPrefix = "/api/v1/persistentvolumes/"
		switch {
		case strings.HasPrefix(r.URL.Path, pvcPrefix):
			claim := strings.TrimPrefix(r.URL.Path, pvcPrefix)
			if _, ok := pvs["pv-"+claim]; !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(model.PersistentVolumeClaim{
				Spec:   model.PersistentVolumeClaimSpec{VolumeName: "pv-" + claim},
				Status: model.PersistentVolumeClaimStatus{Phase: "Bound"},
			})
		case strings.HasPrefix(r.URL.Path, pvPrefix):
			_ = json.NewEncoder(w).Encode(pvs[strings.TrimPrefix(r.URL.Path, pvPrefix)])
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	t.Cleanup(ks.Close)
	kc, _ := kube.New(ks.URL, "", "", false)
	kc.HTTP = ks.Client()
	return &Agent{Kube: kc, Flux: fluxvm.New(fs.URL, ""), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestReconcileDisksAttachesAndDetachesOnlyOwnedDisks(t *testing.T) {
	f := &fakeDeviceFlux{disks: map[string]string{"manual": "/x"}, nics: map[string]string{}}
	a := newDeviceAgent(t, f, map[string]model.PersistentVolume{
		"pv-blk": {Spec: model.PersistentVolumeSpec{VolumeMode: "Block", HostPath: &model.HostPathVolumeSource{Path: "/dev/vg0/data"}}},
		"pv-fs":  {Spec: model.PersistentVolumeSpec{Local: &model.LocalVolumeSource{Path: "/srv/pv/fs"}}},
	})
	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "db", Namespace: "prod", UID: "uid-1"},
		Spec: model.MachineSpec{Disks: []model.MachineDisk{
			{Name: "data", ClaimName: "blk"},
			{Name: "logs", ClaimName: "fs"},
		}},
	}
	disks, _, err := a.reconcileDevices(context.Background(), m, f.record())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(disks, []string{"data", "logs"}) {
		t.Fatalf("attached = %v", disks)
	}
	if f.disks["data"] != "/dev/vg0/data" || f.disks["logs"] != "/srv/pv/fs/disk.img" {
		t.Fatalf("paths = %v", f.disks)
	}

	// Steady state: nothing to do.
	m.Status.AttachedDisks = disks
	f.calls = nil
	if _, _, err := a.reconcileDevices(context.Background(), m, f.record()); err != nil || len(f.calls) != 0 {
		t.Fatalf("steady state made calls %v (err %v)", f.calls, err)
	}

	// Dropping "logs" detaches it; "manual" was never ours and stays.
	m.Spec.Disks = m.Spec.Disks[:1]
	disks, _, err = a.reconcileDevices(context.Background(), m, f.record())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(disks, []string{"data"}) || !slices.Equal(f.calls, []string{"detach logs"}) {
		t.Fatalf("attached = %v, calls = %v", disks, f.calls)
	}
	if _, ok := f.disks["manual"]; !ok {
		t.Fatal("a disk Kairon did not attach was detached")
	}
}

func TestReconcileDisksReportsUnresolvableClaimsWithoutBlockingOthers(t *testing.T) {
	f := &fakeDeviceFlux{disks: map[string]string{}, nics: map[string]string{}}
	a := newDeviceAgent(t, f, map[string]model.PersistentVolume{
		"pv-ok":  {Spec: model.PersistentVolumeSpec{HostPath: &model.HostPathVolumeSource{Path: "/srv/ok"}}},
		"pv-csi": {Metadata: model.ObjectMeta{Name: "pv-csi"}, Spec: model.PersistentVolumeSpec{CSI: &model.CSIPersistentVolumeSource{Driver: "x"}}},
	})
	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "db", Namespace: "prod"},
		Spec: model.MachineSpec{Disks: []model.MachineDisk{
			{Name: "csi", ClaimName: "csi"},
			{Name: "ok", ClaimName: "ok"},
		}},
	}
	disks, _, err := a.reconcileDevices(context.Background(), m, f.record())
	if err == nil || !strings.Contains(err.Error(), "CSI-backed") {
		t.Fatalf("want a CSI error, got %v", err)
	}
	if !slices.Equal(disks, []string{"ok"}) {
		t.Fatalf("attached = %v", disks)
	}
}

func TestReconcileInterfacesHotplugsAndUnplugsByMAC(t *testing.T) {
	f := &fakeDeviceFlux{disks: map[string]string{}, nics: map[string]string{}}
	a := newDeviceAgent(t, f, nil)
	m := model.Machine{
		Metadata: model.ObjectMeta{Name: "fw", Namespace: "prod", UID: "uid-7"},
		Spec: model.MachineSpec{Network: model.NetworkSpec{Mode: "tap", Bridge: "br0", ExtraInterfaces: []model.ExtraInterface{
			{Name: "lan", Bridge: "br-lan"},
			{Name: "dmz", Bridge: "br-dmz", MAC: "02:AA:BB:CC:DD:EE"},
		}}},
	}
	_, ifaces, err := a.reconcileDevices(context.Background(), m, f.record())
	if err != nil {
		t.Fatal(err)
	}
	lanMAC := model.ExtraInterfaceMAC("uid-7", model.ExtraInterface{Name: "lan"})
	if len(ifaces) != 2 || ifaces[0].MAC != lanMAC || ifaces[1].MAC != "02:aa:bb:cc:dd:ee" {
		t.Fatalf("ifaces = %+v", ifaces)
	}
	if f.nics[lanMAC] != "br-lan" || f.nics["02:aa:bb:cc:dd:ee"] != "br-dmz" {
		t.Fatalf("nics = %v", f.nics)
	}

	m.Status.AttachedInterfaces = ifaces
	m.Spec.Network.ExtraInterfaces = m.Spec.Network.ExtraInterfaces[1:]
	f.calls = nil
	_, ifaces, err = a.reconcileDevices(context.Background(), m, f.record())
	if err != nil {
		t.Fatal(err)
	}
	if len(ifaces) != 1 || ifaces[0].Name != "dmz" || !slices.Equal(f.calls, []string{"nic- " + lanMAC}) {
		t.Fatalf("ifaces = %+v calls = %v", ifaces, f.calls)
	}
}

func TestReconcileDevicesWaitsForARunningVM(t *testing.T) {
	a := &Agent{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	m := model.Machine{
		Spec:   model.MachineSpec{Disks: []model.MachineDisk{{Name: "d", ClaimName: "c"}}},
		Status: model.MachineStatus{AttachedDisks: []string{"d"}},
	}
	disks, _, err := a.reconcileDevices(context.Background(), m, &fluxvm.Record{Status: "paused"})
	if err != nil || !slices.Equal(disks, []string{"d"}) {
		t.Fatalf("disks = %v err = %v", disks, err)
	}
}

func TestExtraInterfaceValidationAndMAC(t *testing.T) {
	mac := model.ExtraInterfaceMAC("uid", model.ExtraInterface{Name: "a"})
	if mac != model.ExtraInterfaceMAC("uid", model.ExtraInterface{Name: "a"}) || !strings.HasPrefix(mac, "02:") {
		t.Fatalf("mac %q is not stable/locally administered", mac)
	}
	if mac == model.ExtraInterfaceMAC("uid", model.ExtraInterface{Name: "b"}) {
		t.Fatal("different interfaces got the same MAC")
	}
	bad := []model.NetworkSpec{
		{Mode: "user", ExtraInterfaces: []model.ExtraInterface{{Name: "a", Bridge: "b"}}},
		{Mode: "tap", ExtraInterfaces: []model.ExtraInterface{{Name: "a", Bridge: "b"}, {Name: "a", Bridge: "c"}}},
		{Mode: "tap", ExtraInterfaces: []model.ExtraInterface{{Name: "a"}}},
		{Mode: "tap", ExtraInterfaces: []model.ExtraInterface{{Name: "a", Bridge: "b", MAC: "zz"}}},
		{Mode: "tap", ExtraInterfaces: make([]model.ExtraInterface, 4)},
	}
	for i, ns := range bad {
		if model.ValidateExtraInterfaces(ns) == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	if err := model.ValidateDisks([]model.MachineDisk{{Name: "root", ClaimName: "c"}}); err == nil {
		t.Error("disk named root accepted")
	}
	if err := model.ValidateDisks([]model.MachineDisk{{Name: "a", ClaimName: "c"}, {Name: "a", ClaimName: "d"}}); err == nil {
		t.Error("duplicate disk accepted")
	}
}
