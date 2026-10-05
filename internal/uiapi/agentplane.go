// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package uiapi

import (
	"net/http"

	"github.com/zyvorai/kairon/internal/agentplane"
)

// mountAgentPlane registers the read-only agent routes. They do not
// read a namespace query param; the body is a proposal, not a write.
func (s *Server) mountAgentPlane(api *http.ServeMux) {
	api.HandleFunc("POST /api/v1/agent/compile-policy", s.handleAgentCompile)
	api.HandleFunc("POST /api/v1/agent/explain-drops", s.handleAgentDrops)
	api.HandleFunc("POST /api/v1/agent/anomalies", s.handleAgentAnomalies)
	api.HandleFunc("POST /api/v1/agent/cpu-label", s.handleAgentCPU)
	api.HandleFunc("POST /api/v1/agent/claims/step", s.handleAgentStep)
	api.HandleFunc("POST /api/v1/agent/confidential", s.handleAgentConfidential)
	api.HandleFunc("POST /api/v1/agent/gateway", s.handleAgentGateway)
	api.HandleFunc("POST /api/v1/agent/ask", s.handleAgentAsk)
}

func (s *Server) handleAgentAsk(w http.ResponseWriter, r *http.Request) {
	agentplane.Handler().ServeHTTP(w, r)
}

func (s *Server) handleAgentCompile(w http.ResponseWriter, r *http.Request) {
	agentplane.Handler().ServeHTTP(w, r)
}

func (s *Server) handleAgentDrops(w http.ResponseWriter, r *http.Request) {
	agentplane.Handler().ServeHTTP(w, r)
}

func (s *Server) handleAgentAnomalies(w http.ResponseWriter, r *http.Request) {
	agentplane.Handler().ServeHTTP(w, r)
}

func (s *Server) handleAgentCPU(w http.ResponseWriter, r *http.Request) {
	agentplane.Handler().ServeHTTP(w, r)
}

func (s *Server) handleAgentStep(w http.ResponseWriter, r *http.Request) {
	agentplane.Handler().ServeHTTP(w, r)
}

func (s *Server) handleAgentConfidential(w http.ResponseWriter, r *http.Request) {
	agentplane.Handler().ServeHTTP(w, r)
}

func (s *Server) handleAgentGateway(w http.ResponseWriter, r *http.Request) {
	r.URL.Path = "/api/v1/agent/gateway"
	agentplane.Handler().ServeHTTP(w, r)
}
