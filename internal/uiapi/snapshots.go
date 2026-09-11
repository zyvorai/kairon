// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package uiapi

import (
	"net/http"
	"time"

	"github.com/zyvorai/kairon/internal/model"
)

// createSnapshotRequest mirrors cmdSnapshot's flags exactly. No delete
// route -- internal/kube.Client has none for MachineSnapshots either.
type createSnapshotRequest struct {
	Name    string `json:"name"`
	Machine string `json:"machine"`
	Class   string `json:"class"`
}

func (s *Server) handleListSnapshots(w http.ResponseWriter, r *http.Request) {
	items, err := s.Kube.ListMachineSnapshotsNamespace(r.Context(), namespaceParam(r))
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	var req createSnapshotRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Machine == "" {
		writeError(w, http.StatusBadRequest, "machine is required")
		return
	}
	ns := namespaceParam(r)
	if req.Name == "" {
		req.Name = resourceName(req.Machine + "-" + time.Now().UTC().Format("20060102-150405"))
	}
	snapshot := model.MachineSnapshot{
		TypeMeta: model.TypeMeta{APIVersion: model.APIVersion, Kind: model.KindMachineSnapshot},
		Metadata: model.ObjectMeta{Name: req.Name, Namespace: ns},
		Spec:     model.MachineSnapshotSpec{MachineName: req.Machine, VolumeSnapshotClassName: req.Class},
	}
	out, err := s.Kube.CreateMachineSnapshot(r.Context(), ns, snapshot)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}
