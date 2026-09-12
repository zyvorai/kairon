// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package uiapi

import (
	"net/http"

	"github.com/zyvorai/kairon/internal/model"
)

// createMachineRequest mirrors cmd/kaironctl's cmdCreate flags exactly
// (image/cpu/memory/backend/network/netns/forward/hostname/user/ssh-key/
// package/runcmd), not the full MachineSpec -- the UI's create form offers
// the same surface kaironctl create does, not every field a Machine
// manifest could theoretically set.
type createMachineRequest struct {
	Name              string              `json:"name"`
	CPU               string              `json:"cpu"`
	Memory            string              `json:"memory"`
	Image             string              `json:"image"`
	Backend           string              `json:"backend"`
	Network           string              `json:"network"`
	NetNS             bool                `json:"netns"`
	Forwards          []model.PortForward `json:"forwards,omitempty"`
	Hostname          string              `json:"hostname,omitempty"`
	GuestUser         string              `json:"guestUser,omitempty"`
	SSHAuthorizedKeys []string            `json:"sshAuthorizedKeys,omitempty"`
	Packages          []string            `json:"packages,omitempty"`
	RunCmd            []string            `json:"runCmd,omitempty"`
}

func (s *Server) handleListMachines(w http.ResponseWriter, r *http.Request) {
	items, err := s.Kube.ListMachinesNamespace(r.Context(), namespaceParam(r))
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleGetMachine(w http.ResponseWriter, r *http.Request) {
	m, err := s.Kube.GetMachine(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleCreateMachine(w http.ResponseWriter, r *http.Request) {
	var req createMachineRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Name == "" || req.Image == "" {
		writeError(w, http.StatusBadRequest, "name and image are required")
		return
	}
	if req.CPU == "" {
		req.CPU = "2"
	}
	if req.Memory == "" {
		req.Memory = "2Gi"
	}
	if req.Backend == "" {
		req.Backend = "qemu"
	}
	if req.Network == "" {
		req.Network = "user"
	}
	ns := namespaceParam(r)
	m := model.Machine{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachine},
		Metadata: model.ObjectMeta{Name: req.Name, Namespace: ns},
		Spec: model.MachineSpec{
			Image:     model.ImageSpec{Path: req.Image},
			Resources: model.ResourceSpec{CPU: req.CPU, Memory: req.Memory},
			Runtime:   model.RuntimeSpec{Backend: req.Backend},
			Network:   model.NetworkSpec{Mode: req.Network, NetNS: req.NetNS, Forwards: req.Forwards},
			CloudInit: model.CloudInitSpec{
				Hostname:          req.Hostname,
				User:              req.GuestUser,
				SSHAuthorizedKeys: req.SSHAuthorizedKeys,
				Packages:          req.Packages,
				RunCmd:            req.RunCmd,
			},
			PowerState: "Running",
		},
	}
	out, err := s.Kube.CreateMachine(r.Context(), ns, m)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleDeleteMachine(w http.ResponseWriter, r *http.Request) {
	if err := s.Kube.DeleteMachine(r.Context(), r.PathValue("namespace"), r.PathValue("name")); err != nil {
		writeUpstreamError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePowerMachine returns a handler for the given desired powerState --
// shared by the start/stop routes, mirroring cmdPower's single
// spec.powerState merge-patch (internal/kube.Client.PatchMachine).
func (s *Server) handlePowerMachine(state string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		patch := map[string]any{"spec": map[string]any{"powerState": state}}
		if err := s.Kube.PatchMachine(r.Context(), r.PathValue("namespace"), r.PathValue("name"), patch); err != nil {
			writeUpstreamError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
