package shard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDirectoryLeaseOwnershipAndExpiry(t *testing.T) {
	catalog, err := NewCatalog([]Definition{testDefinition("alpha", 1, true)})
	if err != nil {
		t.Fatal(err)
	}
	definition := catalog.Default()
	definition.Capacity = 1000
	catalog, err = NewCatalog([]Definition{definition})
	if err != nil {
		t.Fatal(err)
	}
	directory, err := NewDirectory(catalog, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)

	if got := directory.Snapshot(now)[0]; got.Operating || got.OnlinePlayers != 0 {
		t.Fatalf("unleased status = %+v", got)
	}
	if err := directory.Publish(Heartbeat{
		ShardID: "alpha", InstanceID: "boot-a", Sequence: 1, OnlinePlayers: 700,
	}, now); err != nil {
		t.Fatal(err)
	}
	if got := directory.Snapshot(now)[0]; !got.Operating || got.OnlinePlayers != 700 {
		t.Fatalf("live status = %+v", got)
	}
	if err := directory.Publish(Heartbeat{
		ShardID: "alpha", InstanceID: "boot-b", Sequence: 1, OnlinePlayers: 1,
	}, now.Add(time.Second)); !errors.Is(err, ErrShardAlreadyOwned) {
		t.Fatalf("split-brain publish = %v", err)
	}
	if err := directory.Publish(Heartbeat{
		ShardID: "alpha", InstanceID: "boot-a", Sequence: 1, OnlinePlayers: 701,
	}, now.Add(time.Second)); !errors.Is(err, ErrStaleHeartbeat) {
		t.Fatalf("stale publish = %v", err)
	}
	if got := directory.Snapshot(now.Add(10 * time.Second))[0]; got.Operating || got.OnlinePlayers != 0 {
		t.Fatalf("expired status = %+v", got)
	}
	if err := directory.Publish(Heartbeat{
		ShardID: "alpha", InstanceID: "boot-b", Sequence: 1, OnlinePlayers: 5,
	}, now.Add(10*time.Second)); err != nil {
		t.Fatalf("takeover after expiry: %v", err)
	}
	if err := directory.Release("alpha", "boot-a"); !errors.Is(err, ErrLeaseNotOwned) {
		t.Fatalf("foreign release = %v", err)
	}
	if err := directory.Release("alpha", "boot-b"); err != nil {
		t.Fatalf("owner release: %v", err)
	}
	if got := directory.Snapshot(now.Add(10 * time.Second))[0]; got.Operating {
		t.Fatalf("released shard still operating: %+v", got)
	}
}

func TestPersistentDirectoryPreservesOwnerAcrossAgentRestart(t *testing.T) {
	catalog, err := NewCatalog([]Definition{testDefinition("alpha", 1, true)})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "shard-leases.json")
	first, err := NewPersistentDirectory(catalog, 10*time.Second, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	if err := first.Publish(Heartbeat{
		ShardID:       "alpha",
		InstanceID:    "boot-a",
		Sequence:      7,
		OnlinePlayers: 1,
	}, now); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewPersistentDirectory(catalog, 10*time.Second, path)
	if err != nil {
		t.Fatal(err)
	}
	status := restarted.Snapshot(now.Add(time.Second))[0]
	if !status.Operating || status.OnlinePlayers != 1 {
		t.Fatalf("restored status = %+v", status)
	}
	if err := restarted.Publish(Heartbeat{
		ShardID:       "alpha",
		InstanceID:    "boot-b",
		Sequence:      1,
		OnlinePlayers: 0,
	}, now.Add(time.Second)); !errors.Is(err, ErrShardAlreadyOwned) {
		t.Fatalf("post-restart split brain = %v", err)
	}
	if err := restarted.Publish(Heartbeat{
		ShardID:       "alpha",
		InstanceID:    "boot-a",
		Sequence:      8,
		OnlinePlayers: 1,
	}, now.Add(time.Second)); err != nil {
		t.Fatalf("owner renewal after Agent restart: %v", err)
	}
}

func TestPersistentDirectoryRefusesMalformedState(t *testing.T) {
	catalog, err := NewCatalog([]Definition{testDefinition("alpha", 1, true)})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "shard-leases.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPersistentDirectory(catalog, time.Second, path); err == nil {
		t.Fatal("malformed persisted shard directory was accepted")
	}
}

/*
================
TestPersistentDirectory50Shards50000Players

Proves that a 50-shard cluster with 1,000 online players per shard (50,000 CCU)
publishes, persists, and recovers across Agent restarts.
================
*/
func TestPersistentDirectory50Shards50000Players(t *testing.T) {
	const shardCount = 50
	const playersPerShard = 1000
	defs := make([]Definition, shardCount)
	for i := 0; i < shardCount; i++ {
		id := fmt.Sprintf("shard-%02d", i+1)
		def := testDefinition(id, uint16(i+1), i == 0)
		def.Name = fmt.Sprintf("Shard %02d", i+1)
		def.Capacity = playersPerShard
		def.PublicTransportURL = fmt.Sprintf("/shards/%s", id)
		defs[i] = def
	}
	catalog, err := NewCatalog(defs)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "shard-leases.json")
	dir, err := NewPersistentDirectory(catalog, 10*time.Second, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2000, 0)
	for i := 0; i < shardCount; i++ {
		id := fmt.Sprintf("shard-%02d", i+1)
		if err := dir.Publish(Heartbeat{
			ShardID:       id,
			InstanceID:    fmt.Sprintf("inst-%02d", i+1),
			Sequence:      1,
			OnlinePlayers: playersPerShard,
			Phase:         PhaseAdmitting,
		}, now); err != nil {
			t.Fatalf("Publish(%s): %v", id, err)
		}
	}

	restarted, err := NewPersistentDirectory(catalog, 10*time.Second, path)
	if err != nil {
		t.Fatalf("NewPersistentDirectory(50 shards): %v", err)
	}
	statuses := restarted.Snapshot(now.Add(time.Second))
	if len(statuses) != shardCount {
		t.Fatalf("len(statuses) = %d, want %d", len(statuses), shardCount)
	}
	totalOnline := 0
	for _, st := range statuses {
		if !st.Operating {
			t.Fatalf("shard %s not operating after restart", st.ID)
		}
		totalOnline += st.OnlinePlayers
	}
	if totalOnline != shardCount*playersPerShard {
		t.Fatalf("totalOnline = %d, want %d", totalOnline, shardCount*playersPerShard)
	}
}
