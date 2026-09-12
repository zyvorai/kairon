// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestLoadUsersParsesConfiguredAccounts(t *testing.T) {
	users, err := loadUsers(`[{"username":"alice","passwordHash":"$2a$10$abc"}]`, "")
	if err != nil {
		t.Fatalf("loadUsers: %v", err)
	}
	if len(users) != 1 || users[0].Username != "alice" || users[0].PasswordHash != "$2a$10$abc" {
		t.Fatalf("unexpected users: %+v", users)
	}
}

func TestLoadUsersHashesDefaultAdminPassword(t *testing.T) {
	users, err := loadUsers("", "generated-password")
	if err != nil {
		t.Fatalf("loadUsers: %v", err)
	}
	if len(users) != 1 || users[0].Username != "admin" {
		t.Fatalf("expected one admin user, got %+v", users)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(users[0].PasswordHash), []byte("generated-password")); err != nil {
		t.Fatalf("expected the stored hash to verify against the plaintext password: %v", err)
	}
	if users[0].PasswordHash == "generated-password" {
		t.Fatal("password must be hashed, not stored in plaintext")
	}
}

func TestLoadUsersCombinesConfiguredAndDefaultAdmin(t *testing.T) {
	users, err := loadUsers(`[{"username":"alice","passwordHash":"$2a$10$abc"}]`, "generated-password")
	if err != nil {
		t.Fatalf("loadUsers: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected both accounts, got %+v", users)
	}
}

func TestLoadUsersRejectsMalformedJSON(t *testing.T) {
	if _, err := loadUsers("not json", ""); err == nil {
		t.Fatal("expected an error for malformed $KAIRON_UI_USERS_JSON")
	}
}

func TestHashPasswordPrintsAVerifiableBcryptHash(t *testing.T) {
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	code := hashPassword([]string{"my-test-password"})
	_ = w.Close()
	os.Stdout = orig
	if code != 0 {
		t.Fatalf("expected exit code 0, got %d", code)
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	hash := strings.TrimSpace(buf.String())
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("my-test-password")); err != nil {
		t.Fatalf("printed hash does not verify: %v", err)
	}
}

func TestHashPasswordRequiresExactlyOneArg(t *testing.T) {
	if code := hashPassword(nil); code != 2 {
		t.Fatalf("expected usage error (2) for no args, got %d", code)
	}
	if code := hashPassword([]string{"a", "b"}); code != 2 {
		t.Fatalf("expected usage error (2) for too many args, got %d", code)
	}
}
