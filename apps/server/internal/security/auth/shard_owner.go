/*
===========================================================================

shard_owner.go - tenant shard ownership, check mode, level cap, GM & ban control

Provides the database operations used by the Customer & Shard Management
Panel (sro-panel) and enforced by the Agent (sro-agent): per-shard owner
settings (check/maintenance mode, max level, capacity), per-shard GM
assignment, and per-shard account bans.

===========================================================================
*/
package auth

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ShardBanRow is one banned account entry on a shard.
type ShardBanRow struct {
	ShardID    string    `json:"shardId"`
	AccountID  string    `json:"accountId"`
	Reason     string    `json:"reason"`
	BannedAt   time.Time `json:"bannedAt"`
	BannedAtMS int64     `json:"bannedAtMs"`
}

/*
================
ensureShardOwnerColumns

Upgrades an existing SQLite shards table written before owner_account_id,
check_mode, and max_level were added.
================
*/
func ensureShardOwnerColumns(db *sql.DB) error {
	columns := []struct {
		name string
		ddl  string
	}{
		{
			name: "owner_account_id",
			ddl:  `ALTER TABLE shards ADD COLUMN owner_account_id TEXT NOT NULL DEFAULT ''`,
		},
		{
			name: "check_mode",
			ddl:  `ALTER TABLE shards ADD COLUMN check_mode INTEGER NOT NULL DEFAULT 0 CHECK (check_mode IN (0, 1))`,
		},
		{
			name: "max_level",
			ddl:  `ALTER TABLE shards ADD COLUMN max_level INTEGER NOT NULL DEFAULT 90 CHECK (max_level >= 1 AND max_level <= 140)`,
		},
	}
	for _, col := range columns {
		var count int
		if err := db.QueryRow(
			`SELECT count(*) FROM pragma_table_info('shards') WHERE name = ?`,
			col.name,
		).Scan(&count); err != nil {
			return fmt.Errorf("shards schema inspect %s: %w", col.name, err)
		}
		if count == 0 {
			if _, err := db.Exec(col.ddl); err != nil {
				return fmt.Errorf("shards schema add %s: %w", col.name, err)
			}
		}
	}
	return nil
}

/*
================
UpdateShardOwnerSettings

Updates a shard's display name, capacity, Check (maintenance) mode, and
max level cap in the shards table.
================
*/
func (accounts *Accounts) UpdateShardOwnerSettings(
	ctx context.Context,
	shardID string,
	name string,
	capacity int,
	checkMode bool,
	maxLevel int,
) error {
	shardID = strings.TrimSpace(shardID)
	name = strings.TrimSpace(name)
	if shardID == "" || name == "" {
		return fmt.Errorf("shard id and name are required")
	}
	if capacity < 1 || capacity > 10000 {
		return fmt.Errorf("capacity must be between 1 and 10000")
	}
	if maxLevel < 1 || maxLevel > 140 {
		return fmt.Errorf("maxLevel must be between 1 and 140")
	}
	chk := 0
	if checkMode {
		chk = 1
	}
	res, err := accounts.db.ExecContext(
		ctx,
		`UPDATE shards
		 SET name = $1,
		     capacity = $2,
		     check_mode = $3,
		     max_level = $4,
		     updated_at_ms = $5
		 WHERE id = $6`,
		name,
		capacity,
		chk,
		maxLevel,
		time.Now().UnixMilli(),
		shardID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("shard %q not found", shardID)
	}
	return nil
}

/*
================
GrantShardGM

Grants GM privilege to accountID specifically for shardID without removing
existing GM grants on other shards.
================
*/
func (accounts *Accounts) GrantShardGM(ctx context.Context, accountID, shardID string) error {
	accountID = strings.TrimSpace(accountID)
	shardID = strings.TrimSpace(shardID)
	if accountID == "" || shardID == "" {
		return fmt.Errorf("accountId and shardId are required")
	}
	info, err := accounts.Lookup(accountID)
	if err != nil {
		return err
	}
	next := append([]string{}, info.GMShards...)
	next = append(next, shardID)
	return accounts.SetGMShards(ctx, info.ID, next)
}

/*
================
RevokeShardGM

Revokes GM privilege from accountID specifically on shardID while preserving
GM privileges on any other shards.
================
*/
func (accounts *Accounts) RevokeShardGM(ctx context.Context, accountID, shardID string) error {
	accountID = strings.TrimSpace(accountID)
	shardID = strings.TrimSpace(shardID)
	if accountID == "" || shardID == "" {
		return fmt.Errorf("accountId and shardId are required")
	}
	info, err := accounts.Lookup(accountID)
	if err != nil {
		return err
	}
	var next []string
	for _, s := range info.GMShards {
		if !strings.EqualFold(s, shardID) && s != "*" {
			next = append(next, s)
		}
	}
	return accounts.SetGMShards(ctx, info.ID, next)
}

/*
================
ListShardGMs

Returns the sorted list of account IDs that hold GM privilege on shardID.
================
*/
func (accounts *Accounts) ListShardGMs(shardID string) ([]string, error) {
	shardID = strings.TrimSpace(shardID)
	rows, err := accounts.db.Query(
		`SELECT DISTINCT account_id FROM account_gm_shards WHERE shard_id = $1 OR shard_id = '*'`,
		shardID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := make(map[string]bool)
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, rows.Err()
}

/*
================
BanOnShard

Bans accountID on shardID and records the reason and timestamp.
================
*/
func (accounts *Accounts) BanOnShard(ctx context.Context, shardID, accountID, reason string) error {
	shardID = strings.TrimSpace(shardID)
	accountID = strings.TrimSpace(accountID)
	reason = strings.TrimSpace(reason)
	if shardID == "" || accountID == "" {
		return fmt.Errorf("shardId and accountId are required")
	}
	if reason == "" {
		reason = "Shard owner ban"
	}
	now := time.Now().UnixMilli()
	_, _ = accounts.db.ExecContext(
		ctx,
		`DELETE FROM shard_bans WHERE shard_id = $1 AND lower(account_id) = lower($2)`,
		shardID,
		accountID,
	)
	_, err := accounts.db.ExecContext(
		ctx,
		`INSERT INTO shard_bans (shard_id, account_id, reason, banned_at_ms) VALUES ($1, $2, $3, $4)`,
		shardID,
		accountID,
		reason,
		now,
	)
	return err
}

/*
================
UnbanOnShard

Removes a shard-scoped ban for accountID on shardID.
================
*/
func (accounts *Accounts) UnbanOnShard(ctx context.Context, shardID, accountID string) error {
	_, err := accounts.db.ExecContext(
		ctx,
		`DELETE FROM shard_bans WHERE shard_id = $1 AND lower(account_id) = lower($2)`,
		strings.TrimSpace(shardID),
		strings.TrimSpace(accountID),
	)
	return err
}

/*
================
IsBannedOnShard

Whether accountID is currently banned on shardID.
================
*/
func (accounts *Accounts) IsBannedOnShard(shardID, accountID string) bool {
	var one int
	err := accounts.db.QueryRow(
		`SELECT 1 FROM shard_bans WHERE shard_id = $1 AND lower(account_id) = lower($2) LIMIT 1`,
		strings.TrimSpace(shardID),
		strings.TrimSpace(accountID),
	).Scan(&one)
	return err == nil && one == 1
}

/*
================
ListShardBans

Returns all active account bans on shardID.
================
*/
func (accounts *Accounts) ListShardBans(shardID string) ([]ShardBanRow, error) {
	rows, err := accounts.db.Query(
		`SELECT shard_id, account_id, reason, banned_at_ms
		 FROM shard_bans
		 WHERE shard_id = $1
		 ORDER BY banned_at_ms DESC`,
		strings.TrimSpace(shardID),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ShardBanRow
	for rows.Next() {
		var r ShardBanRow
		if err := rows.Scan(&r.ShardID, &r.AccountID, &r.Reason, &r.BannedAtMS); err != nil {
			return nil, err
		}
		r.BannedAt = time.UnixMilli(r.BannedAtMS).UTC()
		list = append(list, r)
	}
	return list, rows.Err()
}
