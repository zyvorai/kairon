// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package agentplane

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileLog is an append-only JSONL audit trail. Each record carries the
// previous record's hash, so an edited, reordered or removed line breaks
// Verify. Every append is fsynced before it returns.
type FileLog struct {
	mu   sync.Mutex
	path string
	last string
	// Mirror, when set, receives each appended record. A Mirror error is
	// returned to the caller after the local append has succeeded.
	Mirror func(Event) error
}

// OpenFileLog opens or creates path and verifies the existing chain.
func OpenFileLog(path string) (*FileLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	events, err := readChain(path)
	if err != nil {
		return nil, err
	}
	l := &FileLog{path: path}
	if n := len(events); n > 0 {
		l.last = events[n-1].Hash
	}
	return l, nil
}

// Append records e, chains it and writes it durably.
func (l *FileLog) Append(e Event) (Event, error) {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	recorded, err := Record(e)
	if err != nil {
		return Event{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	recorded.PrevHash = l.last
	recorded.Hash = chainHash(recorded)
	line, err := json.Marshal(recorded)
	if err != nil {
		return Event{}, err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return Event{}, err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return Event{}, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return Event{}, err
	}
	if err := f.Close(); err != nil {
		return Event{}, err
	}
	l.last = recorded.Hash
	if l.Mirror != nil {
		if err := l.Mirror(recorded); err != nil {
			return recorded, fmt.Errorf("audit mirror: %w", err)
		}
	}
	return recorded, nil
}

// Replay returns the verified records for claim, or all when claim is "".
func (l *FileLog) Replay(claim string) ([]Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	events, err := readChain(l.path)
	if err != nil {
		return nil, err
	}
	return Replay(events, claim), nil
}

// VerifyFile checks the whole chain and returns the record count.
func VerifyFile(path string) (int, error) {
	events, err := readChain(path)
	return len(events), err
}

func chainHash(e Event) string {
	e.Hash = ""
	b, _ := json.Marshal(e)
	sum := sha256.Sum256(append([]byte(e.PrevHash+"\n"), b...))
	return hex.EncodeToString(sum[:])
}

func readChain(path string) ([]Event, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Event
	prev := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for n := 1; sc.Scan(); n++ {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("audit log %s line %d: %w", path, n, err)
		}
		if e.PrevHash != prev || chainHash(e) != e.Hash {
			return nil, fmt.Errorf("audit log %s line %d: hash chain broken", path, n)
		}
		prev = e.Hash
		out = append(out, e)
	}
	return out, sc.Err()
}
