/*
===========================================================================

shards.go - database-backed shard catalog and shard-scoped GM authority

Stores shard definitions, live population/operating status, and per-shard
account GM mappings (shards and account_gm_shards tables) in the Agent's
SQLite or PostgreSQL database. The deploy-time config/shards.json catalog
seeds the shards table when rows are absent, while existing database rows
keep their configured capacity, endpoints, and enabled state across restarts.

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

// ShardRow is one persisted shard definition in the shards table.
type ShardRow struct {
	ID                 string
	Name               string
	NativeServerID     uint16
	NativeFarmID       uint16
	Capacity           int
	Default            bool
	Test               bool
	Enabled            bool
	ControlURL         string
	TransportURL       string
	PublicTransportURL string
}

const shardsSQLiteSchema = `
CREATE TABLE IF NOT EXISTS shards (
	id                   TEXT    NOT NULL PRIMARY KEY,
	name                 TEXT    NOT NULL,
	native_server_id     INTEGER NOT NULL UNIQUE,
	native_farm_id       INTEGER NOT NULL DEFAULT 0,
	capacity             INTEGER NOT NULL CHECK (capacity >= 1),
	is_default           INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
	is_test              INTEGER NOT NULL DEFAULT 0 CHECK (is_test IN (0, 1)),
	enabled              INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
	control_url          TEXT    NOT NULL,
	transport_url        TEXT    NOT NULL,
	public_transport_url TEXT    NOT NULL DEFAULT '',
	online_players       INTEGER NOT NULL DEFAULT 0 CHECK (online_players >= 0),
	operating            INTEGER NOT NULL DEFAULT 0 CHECK (operating IN (0, 1)),
	updated_at_ms        INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE IF NOT EXISTS account_gm_shards (
	account_id    TEXT    NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	shard_id      TEXT    NOT NULL,
	granted_at_ms INTEGER NOT NULL,
	PRIMARY KEY (account_id, shard_id)
) STRICT;
`

const shardsPostgresSchema = `
CREATE TABLE IF NOT EXISTS shards (
	id                   TEXT     NOT NULL PRIMARY KEY,
	name                 TEXT     NOT NULL,
	native_server_id     INTEGER  NOT NULL UNIQUE,
	native_farm_id       INTEGER  NOT NULL DEFAULT 0,
	capacity             INTEGER  NOT NULL CHECK (capacity >= 1),
	is_default           SMALLINT NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
	is_test              SMALLINT NOT NULL DEFAULT 0 CHECK (is_test IN (0, 1)),
	enabled              SMALLINT NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
	control_url          TEXT     NOT NULL,
	transport_url        TEXT     NOT NULL,
	public_transport_url TEXT     NOT NULL DEFAULT '',
	online_players       INTEGER  NOT NULL DEFAULT 0 CHECK (online_players >= 0),
	operating            SMALLINT NOT NULL DEFAULT 0 CHECK (operating IN (0, 1)),
	updated_at_ms        BIGINT   NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS account_gm_shards (
	account_id    TEXT   NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
	shard_id      TEXT   NOT NULL,
	granted_at_ms BIGINT NOT NULL,
	PRIMARY KEY (account_id, shard_id)
);
`

/*
================
backfillAccountGMShards

Ensures any account with is_gm = 1 or gm_shards <> ” in the accounts table
also has corresponding relational rows in account_gm_shards.
================
*/
func backfillAccountGMShards(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, is_gm, gm_shards FROM accounts WHERE is_gm = 1 OR gm_shards <> ''`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type gmRow struct {
		id     string
		shards []string
	}
	var pending []gmRow
	for rows.Next() {
		var id, raw string
		var isGM int
		if err := rows.Scan(&id, &isGM, &raw); err != nil {
			return err
		}
		shards := parseGMShards(raw, isGM)
		if len(shards) > 0 {
			pending = append(pending, gmRow{id: id, shards: shards})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for _, r := range pending {
		for _, shardID := range r.shards {
			if _, err := db.Exec(
				`INSERT INTO account_gm_shards (account_id, shard_id, granted_at_ms)
				 SELECT $1, $2, $3
				 WHERE NOT EXISTS (
				     SELECT 1 FROM account_gm_shards WHERE account_id = $4 AND shard_id = $5
				 )`,
				r.id, shardID, now, r.id, shardID,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

/*
================
SeedShards

Inserts every shard definition from defs that is not yet stored in the
shards table. Existing rows keep their database-configured capacity, URLs,
and enabled status.
================
*/
func (accounts *Accounts) SeedShards(defs []ShardRow) (int, error) {
	if len(defs) == 0 {
		return 0, nil
	}
	tx, err := accounts.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UnixMilli()
	inserted := 0
	for _, def := range defs {
		isDefault := 0
		if def.Default {
			isDefault = 1
		}
		isTest := 0
		if def.Test {
			isTest = 1
		}
		enabled := 0
		if def.Enabled {
			enabled = 1
		}
		result, err := tx.Exec(
			`INSERT INTO shards (
				id, name, native_server_id, native_farm_id, capacity,
				is_default, is_test, enabled, control_url, transport_url,
				public_transport_url, online_players, operating, updated_at_ms
			)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 0, 0, $12
			WHERE NOT EXISTS (
				SELECT 1 FROM shards WHERE id = $13 OR native_server_id = $14
			)`,
			def.ID,
			def.Name,
			int(def.NativeServerID),
			int(def.NativeFarmID),
			def.Capacity,
			isDefault,
			isTest,
			enabled,
			def.ControlURL,
			def.TransportURL,
			def.PublicTransportURL,
			now,
			def.ID,
			int(def.NativeServerID),
		)
		if err != nil {
			return 0, fmt.Errorf("seed shard %q: %w", def.ID, err)
		}
		if n, _ := result.RowsAffected(); n == 1 {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

/*
================
UpsertShard

Inserts or updates one shard definition in the shards table.
================
*/
func (accounts *Accounts) UpsertShard(ctx context.Context, def ShardRow) error {
	isDefault := 0
	if def.Default {
		isDefault = 1
	}
	isTest := 0
	if def.Test {
		isTest = 1
	}
	enabled := 0
	if def.Enabled {
		enabled = 1
	}
	now := time.Now().UnixMilli()
	tx, err := accounts.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if def.Default {
		if _, err := tx.ExecContext(ctx, `UPDATE shards SET is_default = 0 WHERE id <> $1`, def.ID); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(
		ctx,
		`UPDATE shards
		 SET name = $1,
		     native_server_id = $2,
		     native_farm_id = $3,
		     capacity = $4,
		     is_default = $5,
		     is_test = $6,
		     enabled = $7,
		     control_url = $8,
		     transport_url = $9,
		     public_transport_url = $10,
		     updated_at_ms = $11
		 WHERE id = $12`,
		def.Name,
		int(def.NativeServerID),
		int(def.NativeFarmID),
		def.Capacity,
		isDefault,
		isTest,
		enabled,
		def.ControlURL,
		def.TransportURL,
		def.PublicTransportURL,
		now,
		def.ID,
	)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO shards (
				id, name, native_server_id, native_farm_id, capacity,
				is_default, is_test, enabled, control_url, transport_url,
				public_transport_url, online_players, operating, updated_at_ms
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 0, 0, $12)`,
			def.ID,
			def.Name,
			int(def.NativeServerID),
			int(def.NativeFarmID),
			def.Capacity,
			isDefault,
			isTest,
			enabled,
			def.ControlURL,
			def.TransportURL,
			def.PublicTransportURL,
			now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

/*
================
ShardDefinitions

Returns all shard definitions stored in the database ordered by
native_server_id, id.
================
*/
func (accounts *Accounts) ShardDefinitions() ([]ShardRow, error) {
	rows, err := accounts.db.Query(
		`SELECT id, name, native_server_id, native_farm_id, capacity,
		        is_default, is_test, enabled, control_url, transport_url, public_transport_url
		 FROM shards
		 ORDER BY native_server_id ASC, id ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var defs []ShardRow
	for rows.Next() {
		var (
			def                      ShardRow
			nativeServer, nativeFarm int
			isDefault, isTest, en    int
		)
		if err := rows.Scan(
			&def.ID,
			&def.Name,
			&nativeServer,
			&nativeFarm,
			&def.Capacity,
			&isDefault,
			&isTest,
			&en,
			&def.ControlURL,
			&def.TransportURL,
			&def.PublicTransportURL,
		); err != nil {
			return nil, err
		}
		def.NativeServerID = uint16(nativeServer)
		def.NativeFarmID = uint16(nativeFarm)
		def.Default = isDefault == 1
		def.Test = isTest == 1
		def.Enabled = en == 1
		defs = append(defs, def)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return defs, nil
}

/*
================
UpdateShardStatus

Persists the live online player count and operating state of shardID into the
shards table so database queries and external dashboards see real-time shard
status.
================
*/
func (accounts *Accounts) UpdateShardStatus(shardID string, onlinePlayers int, operating bool) {
	if onlinePlayers < 0 {
		onlinePlayers = 0
	}
	op := 0
	if operating {
		op = 1
	}
	_, _ = accounts.db.Exec(
		`UPDATE shards SET online_players = $1, operating = $2, updated_at_ms = $3 WHERE id = $4`,
		onlinePlayers,
		op,
		time.Now().UnixMilli(),
		shardID,
	)
}

/*
================
parseGMShards
================
*/
func parseGMShards(raw string, legacyIsGM int) []string {
	seen := make(map[string]bool)
	var shards []string
	for _, item := range strings.Split(raw, ",") {
		shardID := strings.TrimSpace(item)
		if shardID == "" || seen[shardID] {
			continue
		}
		seen[shardID] = true
		shards = append(shards, shardID)
	}
	sort.Strings(shards)
	if len(shards) == 0 && legacyIsGM == 1 {
		return []string{"*"}
	}
	return shards
}

/*
================
formatGMShards
================
*/
func formatGMShards(shardIDs []string) []string {
	seen := make(map[string]bool, len(shardIDs))
	var out []string
	for _, raw := range shardIDs {
		for _, part := range strings.Split(raw, ",") {
			shardID := strings.TrimSpace(part)
			if shardID == "" || seen[shardID] {
				continue
			}
			seen[shardID] = true
			out = append(out, shardID)
		}
	}
	sort.Strings(out)
	return out
}

/*
================
accountGMShards

Combines relational rows in account_gm_shards with the accounts.gm_shards
column so GM grants created via SQL in either table are respected.
================
*/
func (accounts *Accounts) accountGMShards(accountID, rawGMShards string, isGM int) []string {
	merged := parseGMShards(rawGMShards, isGM)
	rows, err := accounts.db.Query(
		`SELECT shard_id FROM account_gm_shards WHERE account_id = $1`,
		accountID,
	)
	if err != nil {
		return merged
	}
	defer rows.Close()
	for rows.Next() {
		var shardID string
		if err := rows.Scan(&shardID); err == nil {
			merged = append(merged, shardID)
		}
	}
	return formatGMShards(merged)
}

/*
================
IsGM

Whether accountID holds GM privilege on at least one shard (or all shards).
================
*/
func (accounts *Accounts) IsGM(accountID string) bool {
	var isGM int
	var rawGMShards string
	err := accounts.db.QueryRow(
		`SELECT is_gm, gm_shards FROM accounts WHERE id = $1 AND disabled = 0`,
		accountID,
	).Scan(&isGM, &rawGMShards)
	if err != nil {
		return false
	}
	return len(accounts.accountGMShards(accountID, rawGMShards, isGM)) > 0
}

/*
================
IsGMOnShard

Whether accountID is an enabled GM account for shardID (either because
shardID is present in account_gm_shards / gm_shards or because "*" is
granted).
================
*/
func (accounts *Accounts) IsGMOnShard(accountID, shardID string) bool {
	shardID = strings.TrimSpace(shardID)
	if shardID == "" {
		return false
	}
	var isGM int
	var rawGMShards string
	err := accounts.db.QueryRow(
		`SELECT is_gm, gm_shards FROM accounts WHERE id = $1 AND disabled = 0`,
		accountID,
	).Scan(&isGM, &rawGMShards)
	if err != nil {
		return false
	}
	for _, granted := range accounts.accountGMShards(accountID, rawGMShards, isGM) {
		if granted == "*" || strings.EqualFold(granted, shardID) {
			return true
		}
	}
	return false
}

/*
================
GMAccountIDs

Returns the sorted ids of all enabled accounts that hold GM privilege on at
least one shard.
================
*/
func (accounts *Accounts) GMAccountIDs() []string {
	rows, err := accounts.db.Query(
		`SELECT id FROM accounts
		 WHERE disabled = 0
		   AND (is_gm = 1 OR gm_shards <> '' OR EXISTS (
		       SELECT 1 FROM account_gm_shards WHERE account_gm_shards.account_id = accounts.id
		   ))`,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil
		}
		ids = append(ids, id)
	}
	if rows.Err() != nil {
		return nil
	}
	sort.Strings(ids)
	return ids
}

/*
================
SetGMShards

Sets the exact list of shard IDs where accountID holds GM privilege (for
example ["global-official", "aege"], or ["*"] for all shards, or nil/empty
to revoke GM privilege everywhere), updating both accounts and the relational
account_gm_shards table atomically.
================
*/
func (accounts *Accounts) SetGMShards(ctx context.Context, accountID string, shardIDs []string) error {
	normalized := formatGMShards(shardIDs)
	flag := 0
	if len(normalized) > 0 {
		flag = 1
	}
	joined := strings.Join(normalized, ",")
	tx, err := accounts.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(
		ctx,
		`UPDATE accounts SET is_gm = $1, gm_shards = $2 WHERE id = $3`,
		flag, joined, accountID,
	)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrAccountNotFound
	}
	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM account_gm_shards WHERE account_id = $1`,
		accountID,
	); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for _, shardID := range normalized {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO account_gm_shards (account_id, shard_id, granted_at_ms) VALUES ($1, $2, $3)`,
			accountID, shardID, now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

/*
================
SetGM

Grants ("*") or revokes ([]) GM privilege across all shards.
================
*/
func (accounts *Accounts) SetGM(ctx context.Context, accountID string, isGM bool) error {
	if isGM {
		return accounts.SetGMShards(ctx, accountID, []string{"*"})
	}
	return accounts.SetGMShards(ctx, accountID, nil)
}

/*
================
SilkBalance

Returns the account's global cross-shard Silk balance (own, gift, point).
================
*/
func (accounts *Accounts) SilkBalance(accountID string) (SilkBalance, error) {
	info, err := accounts.Lookup(accountID)
	if err != nil {
		return SilkBalance{}, err
	}
	return info.Silk, nil
}

/*
================
AdjustSilk

Atomically credits or debits the account's global cross-shard Silk balances.
If any resulting bucket would fall below 0, the update is refused with
ErrInsufficientSilk without modifying the row.
================
*/
func (accounts *Accounts) AdjustSilk(
	ctx context.Context,
	accountID string,
	deltaOwn int64,
	deltaGift int64,
	deltaPoint int64,
) (SilkBalance, error) {
	result, err := accounts.db.ExecContext(
		ctx,
		`UPDATE accounts
		 SET silk_own = silk_own + $1,
		     silk_gift = silk_gift + $2,
		     silk_point = silk_point + $3
		 WHERE id = $4
		   AND silk_own + $1 >= 0
		   AND silk_gift + $2 >= 0
		   AND silk_point + $3 >= 0`,
		deltaOwn,
		deltaGift,
		deltaPoint,
		accountID,
	)
	if err != nil {
		return SilkBalance{}, err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		if _, lookupErr := accounts.Lookup(accountID); lookupErr != nil {
			return SilkBalance{}, lookupErr
		}
		return SilkBalance{}, ErrInsufficientSilk
	}
	return accounts.SilkBalance(accountID)
}
