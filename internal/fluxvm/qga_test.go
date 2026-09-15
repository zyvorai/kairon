// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package fluxvm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQGAFsfreezeFreeze(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/vms/vm-1/qga/fsfreeze" {
			http.Error(w, "bad route", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]int64{"frozen": 2})
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	n, err := c.QGAFsfreezeFreeze(context.Background(), "vm-1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 filesystems frozen, got %d", n)
	}
}

func TestQGAFsfreezeThaw(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/vms/vm-1/qga/fsthaw" {
			http.Error(w, "bad route", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]int64{"thawed": 2})
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	n, err := c.QGAFsfreezeThaw(context.Background(), "vm-1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected 2 filesystems thawed, got %d", n)
	}
}

func TestQGAFsfreezePropagatesErrors(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "QGA not enabled for this VM", http.StatusBadRequest)
	}))
	defer s.Close()
	c := New(s.URL, "")
	c.HTTP = s.Client()
	if _, err := c.QGAFsfreezeFreeze(context.Background(), "vm-1"); err == nil {
		t.Fatal("expected an error when the server rejects the request")
	}
	if _, err := c.QGAFsfreezeThaw(context.Background(), "vm-1"); err == nil {
		t.Fatal("expected an error when the server rejects the request")
	}
}
