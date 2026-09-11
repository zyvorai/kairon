// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package migration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	Store    Store
	Driver   DestinationDriver
	NodeName string
	Now      func() time.Time
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/v1/migrations/prepare", s.prepare)
	mux.HandleFunc("GET /internal/v1/migrations/{id}", s.get)
	mux.HandleFunc("POST /internal/v1/migrations/{id}/commit", s.commit)
	mux.HandleFunc("POST /internal/v1/migrations/{id}/abort", s.abort)
	mux.HandleFunc("GET /internal/v1/migrations/{id}/diagnosis", s.diagnosis)
	return http.MaxBytesHandler(mux, 8<<20)
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Server) dependencies() error {
	if s.Store == nil {
		return errors.New("migration store is not configured")
	}
	if s.Driver == nil {
		return errors.New("migration destination driver is not configured")
	}
	return nil
}

func (s *Server) prepare(w http.ResponseWriter, r *http.Request) {
	if err := s.dependencies(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	var req PrepareRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode prepare request: %w", err))
		return
	}
	if err := ensureEOF(dec); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := req.Session.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.NodeName != "" && req.Session.TargetNode != s.NodeName {
		writeError(w, http.StatusForbidden, fmt.Errorf("session targets node %q but this peer is %q", req.Session.TargetNode, s.NodeName))
		return
	}

	existing, err := s.Store.Get(req.Session.ID)
	if err == nil {
		if !existing.SameIdentity(req.Session) {
			writeError(w, http.StatusConflict, errors.New("session id already exists with different immutable identity"))
			return
		}
		writeJSON(w, http.StatusOK, response(existing))
		return
	}
	if !errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	result, err := s.Driver.Prepare(r.Context(), req.Session)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("prepare destination: %w", err))
		return
	}
	if result.TransferSupported && strings.TrimSpace(result.Endpoint) == "" {
		writeError(w, http.StatusBadGateway, errors.New("destination driver claimed support but returned an empty endpoint"))
		return
	}
	now := s.now()
	session := req.Session
	session.CreatedAt, session.UpdatedAt = now, now
	session.TransferSupported = result.TransferSupported
	session.Endpoint = result.Endpoint
	session.Backend = result.Backend
	session.Reason = result.Reason
	if result.TransferSupported {
		session.Phase = "Prepared"
	} else {
		session.Phase = "Unsupported"
	}
	if err := s.Store.Put(session); err != nil {
		_ = s.Driver.Abort(r.Context(), session)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, response(session))
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("migration store is not configured"))
		return
	}
	session, err := s.Store.Get(r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, response(session))
}

func (s *Server) commit(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, "Committed", func(session Session) error { return s.Driver.Commit(r.Context(), session) })
}

func (s *Server) abort(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, "Aborted", func(session Session) error { return s.Driver.Abort(r.Context(), session) })
}

func (s *Server) transition(w http.ResponseWriter, r *http.Request, desired string, fn func(Session) error) {
	if err := s.dependencies(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	session, err := s.Store.Get(r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if session.Phase == desired {
		writeJSON(w, http.StatusOK, response(session))
		return
	}
	if desired == "Committed" && session.Phase != "Prepared" {
		writeError(w, http.StatusConflict, fmt.Errorf("cannot commit session in phase %s", session.Phase))
		return
	}
	if desired == "Aborted" && session.Phase == "Committed" {
		writeError(w, http.StatusConflict, errors.New("cannot abort a committed session"))
		return
	}
	if err := fn(session); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	session.Phase = desired
	session.UpdatedAt = s.now()
	if err := s.Store.Put(session); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, response(session))
}

// diagnosis is strictly read-only (never mutates Store) so it's always
// safe to poll, including on every NeedsRecovery reconcile tick -- it
// answers "what do we actually know" without requiring or implying any
// recovery action has been decided.
func (s *Server) diagnosis(w http.ResponseWriter, r *http.Request) {
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("migration store is not configured"))
		return
	}
	session, err := s.Store.Get(r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	diagnosable, ok := s.Driver.(Diagnosable)
	if !ok {
		writeJSON(w, http.StatusOK, DiagnosisResult{SessionPhase: session.Phase, ObservedAt: s.now()})
		return
	}
	result, err := diagnosable.Diagnose(r.Context(), session)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("diagnose destination: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func response(session Session) PrepareResponse {
	return PrepareResponse{SessionID: session.ID, Phase: session.Phase, TransferSupported: session.TransferSupported, Endpoint: session.Endpoint, Backend: session.Backend, Reason: session.Reason}
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("request must contain exactly one JSON object")
		}
		return fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
