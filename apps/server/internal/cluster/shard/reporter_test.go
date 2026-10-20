package shard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestReporterAcquireWaitsThroughTransientLeaseContention(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/cluster/shards/heartbeat" {
			http.NotFound(w, r)
			return
		}
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"LEASE_REFUSED"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	identityFile := filepath.Join(t.TempDir(), "identity.jwt")
	if err := os.WriteFile(identityFile, []byte("workload-identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	reporter, err := NewReporter(server.URL, identityFile, "test", func() int { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	if err := reporter.Acquire(context.Background(), 2*time.Second); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("heartbeat attempts = %d, want 3", got)
	}
}

func TestReporterReadsAuthenticatedAccountDirectory(t *testing.T) {
	const identity = "workload-identity"
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/internal/accounts" {
				http.NotFound(w, r)
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+identity {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if r.Header.Get(ControlShardHeader) != "test" {
				http.Error(w, "wrong shard", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"accountIds":["alice","bob"]}`))
		},
	))
	t.Cleanup(server.Close)
	identityFile := filepath.Join(t.TempDir(), "identity.jwt")
	if err := os.WriteFile(identityFile, []byte(identity), 0o600); err != nil {
		t.Fatal(err)
	}
	reporter, err := NewReporter(
		server.URL,
		identityFile,
		"test",
		func() int { return 0 },
	)
	if err != nil {
		t.Fatal(err)
	}
	accountIDs, err := reporter.AccountIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accountIDs) != 2 ||
		accountIDs[0] != "alice" ||
		accountIDs[1] != "bob" {
		t.Fatalf("account ids = %v", accountIDs)
	}
}

func TestReporterRefusesMalformedAccountDirectory(t *testing.T) {
	for name, body := range map[string]string{
		"empty":     `{"accountIds":[]}`,
		"duplicate": `{"accountIds":["alice","alice"]}`,
		"invalid":   `{"accountIds":[" padded "]}`,
		"unknown":   `{"accountIds":["alice"],"hashes":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(body))
				},
			))
			t.Cleanup(server.Close)
			identityFile := filepath.Join(t.TempDir(), "identity.jwt")
			if err := os.WriteFile(
				identityFile,
				[]byte("workload-identity"),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			reporter, err := NewReporter(
				server.URL,
				identityFile,
				"test",
				func() int { return 0 },
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reporter.AccountIDs(context.Background()); err == nil {
				t.Fatalf("body %s accepted", body)
			}
		})
	}
}

/*
================
TestReporterReads50000AccountDirectory

Proves that GameWorld startup can load a 50,000-account directory (>700 KB JSON)
without tripping the 64 KB heartbeat cap.
================
*/
func TestReporterReads50000AccountDirectory(t *testing.T) {
	const totalAccounts = 50_000
	expected := make([]string, totalAccounts)
	for i := 0; i < totalAccounts; i++ {
		expected[i] = fmt.Sprintf("player_%05d", i)
	}
	payload, err := json.Marshal(struct {
		AccountIDs []string `json:"accountIds"`
	}{AccountIDs: expected})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)

	identityFile := filepath.Join(t.TempDir(), "identity.jwt")
	if err := os.WriteFile(identityFile, []byte("workload-identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	reporter, err := NewReporter(server.URL, identityFile, "test", func() int { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	got, err := reporter.AccountIDs(context.Background())
	if err != nil {
		t.Fatalf("AccountIDs(50000): %v", err)
	}
	if len(got) != totalAccounts {
		t.Fatalf("len(AccountIDs) = %d, want %d", len(got), totalAccounts)
	}
}
