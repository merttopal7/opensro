/*
===========================================================================

accounts.go - the live global account authority

The Agent is the only holder of login credentials. Accounts live in one
SQLite database the Agent owns, so a new account (a website signup) takes
effect at once, with no redeploy. The deploy-time catalog (catalog.go) is a
seed: its rows are inserted when absent and never overwrite a stored
account, so a password changed here is not reverted by the next deploy.

Account ids are stored exactly as created, and lookups are exact, because
characters are owned by the exact id. A unique index on the ASCII-folded id
stops a second account that differs only in case ("Bob" beside "bob").

A disabled account keeps its row (its characters still have an owner) but
cannot log in: PasswordHash reports it absent, which also ends its browser
sessions at their next check (browser_session.go).

===========================================================================
*/
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
	"opensro.online/server/internal/domain"
)

const (
	// AccountBcryptCost is the cost of hashes this store creates. It stays
	// inside the catalog's accepted range so a store row can always be
	// exported back into a seed catalog.
	AccountBcryptCost = MaxBcryptCost

	// MinPasswordBytes and MaxPasswordBytes bound new passwords. bcrypt
	// ignores bytes past 72, so a longer password would silently truncate.
	MinPasswordBytes = 8
	MaxPasswordBytes = 72

	// AgentProvisioningTokenFile is the cluster-state file holding the
	// provisioning API bearer token (internal/agent/provisioning), and
	// MinProvisioningTokenBytes the shortest token the API accepts.
	AgentProvisioningTokenFile = "agent-provisioning-token"
	MinProvisioningTokenBytes  = 32

	// PublicAPITokenFile is the cluster-state file holding the GameWorld
	// public API's privacy-write bearer token (internal/agent/publicstats).
	// It is its own secret, never the provisioning token, so a leak of one
	// does not open the other.
	PublicAPITokenFile = "public-api-token"

	accountsSchemaVersion = 1
	accountsBusyTimeoutMS = 5000
)

var (
	ErrAccountExists    = errors.New("account already exists")
	ErrAccountNotFound  = errors.New("account not found")
	ErrAccountIDInvalid = errors.New("account id is invalid")
	ErrPasswordInvalid  = fmt.Errorf("password must be %d to %d bytes", MinPasswordBytes, MaxPasswordBytes)
	ErrInsufficientSilk = errors.New("insufficient silk balance")
)

// SilkBalance holds the global cross-shard Item Mall currency buckets
// (SR_AccountDB SK_Silk: silk_own, silk_gift, silk_point).
type SilkBalance struct {
	Own   int64 `json:"own"`
	Gift  int64 `json:"gift"`
	Point int64 `json:"point"`
}

// AccountInfo is the public view of one account: never its hash.
type AccountInfo struct {
	ID        string
	CreatedAt time.Time
	Disabled  bool
	IsGM      bool
	GMShards  []string
	Silk      SilkBalance
}

// Accounts is the SQL-backed (SQLite or PostgreSQL) global account authority.
type Accounts struct {
	db *sql.DB

	// hashing bounds concurrent bcrypt work so account creation cannot
	// starve logins of CPU.
	hashing chan struct{}

	// cost is the bcrypt cost of the hashes this store creates, and of its
	// unknown-account dummy (AccountsOptions).
	cost      int
	dummyOnce sync.Once
	dummy     []byte

	revokeMu sync.RWMutex
	onRevoke func(accountID string, epoch uint64)

	closeOnce sync.Once
}

/*
================
AccountsOptions

How OpenAccountsWith opens a store. The zero value is production's.
================
*/
type AccountsOptions struct {
	// BcryptCost is the cost of new hashes; zero is AccountBcryptCost.
	// Tests pass bcrypt.MinCost so a login does not cost a production hash
	// under the race detector; production never sets it.
	BcryptCost int
}

const accountsSchema = `
CREATE TABLE IF NOT EXISTS accounts (
	id            TEXT    NOT NULL PRIMARY KEY,
	password_hash BLOB    NOT NULL,
	created_at_ms INTEGER NOT NULL,
	disabled      INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1)),
	is_gm         INTEGER NOT NULL DEFAULT 0 CHECK (is_gm IN (0, 1)),
	gm_shards     TEXT    NOT NULL DEFAULT '',
	session_epoch INTEGER NOT NULL DEFAULT 1,
	silk_own      INTEGER NOT NULL DEFAULT 0 CHECK (silk_own >= 0),
	silk_gift     INTEGER NOT NULL DEFAULT 0 CHECK (silk_gift >= 0),
	silk_point    INTEGER NOT NULL DEFAULT 0 CHECK (silk_point >= 0)
) STRICT;
CREATE UNIQUE INDEX IF NOT EXISTS accounts_folded_id ON accounts (lower(id));
`

const accountsPostgresSchema = `
CREATE TABLE IF NOT EXISTS accounts (
	id            TEXT     NOT NULL PRIMARY KEY,
	password_hash BYTEA    NOT NULL,
	created_at_ms BIGINT   NOT NULL,
	disabled      SMALLINT NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1)),
	is_gm         SMALLINT NOT NULL DEFAULT 0 CHECK (is_gm IN (0, 1)),
	gm_shards     TEXT     NOT NULL DEFAULT '',
	session_epoch BIGINT   NOT NULL DEFAULT 1,
	silk_own      BIGINT   NOT NULL DEFAULT 0 CHECK (silk_own >= 0),
	silk_gift     BIGINT   NOT NULL DEFAULT 0 CHECK (silk_gift >= 0),
	silk_point    BIGINT   NOT NULL DEFAULT 0 CHECK (silk_point >= 0)
);
CREATE UNIQUE INDEX IF NOT EXISTS accounts_folded_id ON accounts (lower(id));
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS is_gm SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS gm_shards TEXT NOT NULL DEFAULT '';
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS session_epoch BIGINT NOT NULL DEFAULT 1;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS silk_own BIGINT NOT NULL DEFAULT 0;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS silk_gift BIGINT NOT NULL DEFAULT 0;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS silk_point BIGINT NOT NULL DEFAULT 0;
`

/*
================
OpenAccounts

Opens (creating when absent) the account database at path. The database is
the login authority, so it must be a regular file: symlinks are refused as
catalog.go refuses them.
================
*/
func OpenAccounts(path string) (*Accounts, error) {
	return OpenAccountsWith(path, AccountsOptions{})
}

/*
================
OpenAccountsWith

OpenAccounts with explicit options.
================
*/
func OpenAccountsWith(path string, options AccountsOptions) (*Accounts, error) {
	cost := options.BcryptCost
	if cost == 0 {
		cost = AccountBcryptCost
	}
	if cost < bcrypt.MinCost || cost > MaxBcryptCost {
		return nil, fmt.Errorf("account bcrypt cost %d is outside %d..%d", cost, bcrypt.MinCost, MaxBcryptCost)
	}
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("account database path is empty")
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("account database %s is not a regular file", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection serializes writers; account traffic is tiny.
	db.SetMaxOpenConns(1)
	pragmas := []string{
		fmt.Sprintf("PRAGMA busy_timeout=%d", accountsBusyTimeoutMS),
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=FULL",
		"PRAGMA foreign_keys=ON",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		_ = db.Close()
		return nil, err
	}
	if version > accountsSchemaVersion {
		_ = db.Close()
		return nil, fmt.Errorf(
			"account database schema %d is newer than this Agent (%d)",
			version,
			accountsSchemaVersion,
		)
	}
	if _, err := db.Exec(accountsSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("account schema: %w", err)
	}
	if err := ensureAccountSessionEpoch(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureAccountGMColumn(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureAccountSilkColumns(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(shardsSQLiteSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("shards schema: %w", err)
	}
	if err := ensureShardOwnerColumns(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := backfillAccountGMShards(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("backfill account_gm_shards: %w", err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version=%d", accountsSchemaVersion)); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Accounts{
		db:      db,
		hashing: make(chan struct{}, max(2, runtime.NumCPU())),
		cost:    cost,
	}, nil
}

/*
================
OpenAccountsPostgres

Opens the global account authority backed by PostgreSQL (port-only, not native).
Used when SRO_AGENT_POSTGRES_DSN is configured so multi-shard clusters share
one ACID account and Silk authority across all nodes and web portals.
================
*/
func OpenAccountsPostgres(dsn string) (*Accounts, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("account postgres dsn is empty")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	if _, err := db.ExecContext(ctx, accountsPostgresSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres account schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, shardsPostgresSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres shards schema: %w", err)
	}
	if err := backfillAccountGMShards(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres backfill account_gm_shards: %w", err)
	}
	return &Accounts{
		db:      db,
		hashing: make(chan struct{}, max(2, runtime.NumCPU())),
		cost:    AccountBcryptCost,
	}, nil
}

/*
================
Close
================
*/
func (accounts *Accounts) Close() error {
	var err error
	accounts.closeOnce.Do(func() {
		err = accounts.db.Close()
	})
	return err
}

/*
================
ensureAccountSessionEpoch

Upgrades an existing accounts.db written before session_epoch was added so
every account starts at epoch 1 without requiring a manual migration.
================
*/
func ensureAccountSessionEpoch(db *sql.DB) error {
	var count int
	if err := db.QueryRow(
		`SELECT count(*) FROM pragma_table_info('accounts') WHERE name = 'session_epoch'`,
	).Scan(&count); err != nil {
		return fmt.Errorf("account schema inspect: %w", err)
	}
	if count > 0 {
		return nil
	}
	if _, err := db.Exec(
		`ALTER TABLE accounts ADD COLUMN session_epoch INTEGER NOT NULL DEFAULT 1`,
	); err != nil {
		return fmt.Errorf("account schema session_epoch: %w", err)
	}
	return nil
}

/*
================
ensureAccountGMColumn

Upgrades an existing SQLite accounts.db written before is_gm and gm_shards
were added so every account defaults to non-GM (0 / empty shard list).
================
*/
func ensureAccountGMColumn(db *sql.DB) error {
	var count int
	if err := db.QueryRow(
		`SELECT count(*) FROM pragma_table_info('accounts') WHERE name = 'is_gm'`,
	).Scan(&count); err != nil {
		return fmt.Errorf("account schema inspect is_gm: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(
			`ALTER TABLE accounts ADD COLUMN is_gm INTEGER NOT NULL DEFAULT 0 CHECK (is_gm IN (0, 1))`,
		); err != nil {
			return fmt.Errorf("account schema is_gm: %w", err)
		}
	}
	if err := db.QueryRow(
		`SELECT count(*) FROM pragma_table_info('accounts') WHERE name = 'gm_shards'`,
	).Scan(&count); err != nil {
		return fmt.Errorf("account schema inspect gm_shards: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(
			`ALTER TABLE accounts ADD COLUMN gm_shards TEXT NOT NULL DEFAULT ''`,
		); err != nil {
			return fmt.Errorf("account schema gm_shards: %w", err)
		}
	}
	return nil
}

/*
================
ensureAccountSilkColumns

Upgrades an existing SQLite accounts.db written before silk_own, silk_gift,
and silk_point were added so every account starts with 0 Silk across all
three global currency buckets.
================
*/
func ensureAccountSilkColumns(db *sql.DB) error {
	for _, column := range []string{"silk_own", "silk_gift", "silk_point"} {
		var count int
		if err := db.QueryRow(
			`SELECT count(*) FROM pragma_table_info('accounts') WHERE name = $1`,
			column,
		).Scan(&count); err != nil {
			return fmt.Errorf("account schema inspect %s: %w", column, err)
		}
		if count > 0 {
			continue
		}
		if _, err := db.Exec(
			fmt.Sprintf(`ALTER TABLE accounts ADD COLUMN %s INTEGER NOT NULL DEFAULT 0 CHECK (%s >= 0)`, column, column),
		); err != nil {
			return fmt.Errorf("account schema %s: %w", column, err)
		}
	}
	return nil
}

/*
================
MigrateFromSQLite

Imports any account rows present in a local SQLite accounts.db that are not
yet present in this store (preserving id, password_hash, created_at_ms,
disabled, session_epoch, and silk_*). Used when switching an existing
cluster from SQLite to PostgreSQL without losing existing player accounts.
================
*/
func (accounts *Accounts) MigrateFromSQLite(sqlitePath string) (int, error) {
	if strings.TrimSpace(sqlitePath) == "" {
		return 0, nil
	}
	if info, err := os.Lstat(sqlitePath); err != nil || !info.Mode().IsRegular() {
		return 0, nil
	}
	source, err := OpenAccounts(sqlitePath)
	if err != nil {
		return 0, fmt.Errorf("open source sqlite %s: %w", sqlitePath, err)
	}
	defer source.Close()

	rows, err := source.db.Query(
		`SELECT id, password_hash, created_at_ms, disabled, is_gm, gm_shards, session_epoch, silk_own, silk_gift, silk_point FROM accounts`,
	)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type migratedRow struct {
		id           string
		passwordHash []byte
		createdAtMS  int64
		disabled     int
		isGM         int
		gmShards     string
		sessionEpoch int64
		silkOwn      int64
		silkGift     int64
		silkPoint    int64
	}
	var pending []migratedRow
	for rows.Next() {
		var r migratedRow
		if err := rows.Scan(
			&r.id,
			&r.passwordHash,
			&r.createdAtMS,
			&r.disabled,
			&r.isGM,
			&r.gmShards,
			&r.sessionEpoch,
			&r.silkOwn,
			&r.silkGift,
			&r.silkPoint,
		); err != nil {
			return 0, err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	tx, err := accounts.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	inserted := 0
	for _, r := range pending {
		result, err := tx.Exec(
			`INSERT INTO accounts (id, password_hash, created_at_ms, disabled, is_gm, gm_shards, session_epoch, silk_own, silk_gift, silk_point)
			 SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
			 WHERE NOT EXISTS (SELECT 1 FROM accounts WHERE lower(id) = lower($11))`,
			r.id,
			r.passwordHash,
			r.createdAtMS,
			r.disabled,
			r.isGM,
			r.gmShards,
			r.sessionEpoch,
			r.silkOwn,
			r.silkGift,
			r.silkPoint,
			r.id,
		)
		if err != nil {
			return 0, fmt.Errorf("migrate account %q: %w", r.id, err)
		}
		if n, _ := result.RowsAffected(); n == 1 {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if err := backfillAccountGMShards(accounts.db); err != nil {
		return 0, err
	}
	return inserted, nil
}

/*
================
SetRevokeObserver

Installs an observer called whenever an account's session epoch advances
(SetPassword, SetDisabled, RevokeSessions).
================
*/
func (accounts *Accounts) SetRevokeObserver(fn func(accountID string, epoch uint64)) {
	accounts.revokeMu.Lock()
	accounts.onRevoke = fn
	accounts.revokeMu.Unlock()
}

/*
================
notifyRevoke
================
*/
func (accounts *Accounts) notifyRevoke(accountID string, epoch uint64) {
	accounts.revokeMu.RLock()
	fn := accounts.onRevoke
	accounts.revokeMu.RUnlock()
	if fn != nil {
		fn(accountID, epoch)
	}
}

/*
================
Seed

Inserts every catalog account the store does not already hold, under its
catalog hash. Existing rows, including any whose id differs only in case,
are left alone. Returns how many rows were inserted.
================
*/
func (accounts *Accounts) Seed(catalog *Catalog) (int, error) {
	if catalog == nil {
		return 0, nil
	}
	tx, err := accounts.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	inserted := 0
	for _, id := range catalog.IDs() {
		hash, _ := catalog.PasswordHash(id)
		result, err := tx.Exec(
			`INSERT INTO accounts (id, password_hash, created_at_ms)
			 SELECT $1, $2, $3
			 WHERE NOT EXISTS (SELECT 1 FROM accounts WHERE lower(id) = lower($4))`,
			id, hash, now, id,
		)
		if err != nil {
			return 0, fmt.Errorf("seed account %q: %w", id, err)
		}
		if rows, _ := result.RowsAffected(); rows == 1 {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if err := backfillAccountGMShards(accounts.db); err != nil {
		return 0, err
	}
	return inserted, nil
}

/*
================
PasswordHash

The login hash for an enabled account. A missing or disabled account, or a
database error, reports false: the authority fails closed.
================
*/
func (accounts *Accounts) PasswordHash(accountID string) ([]byte, bool) {
	var hash []byte
	err := accounts.db.QueryRow(
		`SELECT password_hash FROM accounts WHERE id = $1 AND disabled = 0`,
		accountID,
	).Scan(&hash)
	if err != nil {
		return nil, false
	}
	return hash, true
}

/*
================
SessionEpoch

The current session epoch for an enabled account. A missing or disabled
account, or a database error, reports false: the authority fails closed.
================
*/
func (accounts *Accounts) SessionEpoch(accountID string) (uint64, bool) {
	var epoch int64
	err := accounts.db.QueryRow(
		`SELECT session_epoch FROM accounts WHERE id = $1 AND disabled = 0`,
		accountID,
	).Scan(&epoch)
	if err != nil || epoch < 1 {
		return 0, false
	}
	return uint64(epoch), true
}

/*
================
RevokedEpochs

Returns every account whose session epoch has advanced past the initial
epoch (1), including disabled accounts whose sessions were revoked when
they were disabled.
================
*/
func (accounts *Accounts) RevokedEpochs() map[string]uint64 {
	rows, err := accounts.db.Query(
		`SELECT id, session_epoch FROM accounts WHERE session_epoch >= 2`,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	epochs := make(map[string]uint64)
	for rows.Next() {
		var id string
		var epoch int64
		if err := rows.Scan(&id, &epoch); err == nil && epoch >= 2 {
			epochs[id] = uint64(epoch)
		}
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return epochs
}

/*
================
readEpoch
================
*/
func (accounts *Accounts) readEpoch(ctx context.Context, accountID string) (uint64, error) {
	var epoch int64
	if err := accounts.db.QueryRowContext(
		ctx,
		`SELECT session_epoch FROM accounts WHERE id = $1`,
		accountID,
	).Scan(&epoch); err != nil {
		return 0, err
	}
	if epoch < 1 {
		return 1, nil
	}
	return uint64(epoch), nil
}

/*
================
Credential

Resolves the id a player typed at login to the stored account and its login
hash. The match ignores ASCII case, as the accounts_folded_id index already
makes ids unique that way, so the website's lowercase "demiurgs" and a typed
"Demiurgs" are one account. The stored id is returned: characters are
owned by it, so the session must carry it, never the typed spelling.
A missing or disabled account, or a database error, reports false.
================
*/
func (accounts *Accounts) Credential(typedID string) (string, []byte, bool) {
	var id string
	var hash []byte
	err := accounts.db.QueryRow(
		`SELECT id, password_hash FROM accounts WHERE lower(id) = lower($1) AND disabled = 0`,
		typedID,
	).Scan(&id, &hash)
	if err != nil {
		return "", nil, false
	}
	return id, hash, true
}

/*
================
IDs

Every account id, disabled ones included: GameWorld uses the set to prove
each durable character has an owner, and a disabled owner still owns.
Returns nil on a database error, which GameWorld treats as a refusal.
================
*/
func (accounts *Accounts) IDs() []string {
	rows, err := accounts.db.Query(`SELECT id FROM accounts`)
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
Len
================
*/
func (accounts *Accounts) Len() int {
	var count int
	if err := accounts.db.QueryRow(`SELECT count(*) FROM accounts`).Scan(&count); err != nil {
		return 0
	}
	return count
}

/*
================
Lookup
================
*/
func (accounts *Accounts) Lookup(accountID string) (AccountInfo, error) {
	var createdAt int64
	var disabled int
	var isGM int
	var rawGMShards string
	var silk SilkBalance
	err := accounts.db.QueryRow(
		`SELECT created_at_ms, disabled, is_gm, gm_shards, silk_own, silk_gift, silk_point FROM accounts WHERE id = $1`,
		accountID,
	).Scan(&createdAt, &disabled, &isGM, &rawGMShards, &silk.Own, &silk.Gift, &silk.Point)
	if errors.Is(err, sql.ErrNoRows) {
		return AccountInfo{}, ErrAccountNotFound
	}
	if err != nil {
		return AccountInfo{}, err
	}
	gmShards := accounts.accountGMShards(accountID, rawGMShards, isGM)
	return AccountInfo{
		ID:        accountID,
		CreatedAt: time.UnixMilli(createdAt).UTC(),
		Disabled:  disabled == 1,
		IsGM:      len(gmShards) > 0,
		GMShards:  gmShards,
		Silk:      silk,
	}, nil
}

/*
================
Create

Creates an enabled account. The id must satisfy domain.AccountIDValid, must
not be reserved, and must not match an existing id case-insensitively.
================
*/
func (accounts *Accounts) Create(ctx context.Context, accountID, password string) (AccountInfo, error) {
	if !domain.AccountIDValid(accountID) || accountID == domain.ReservedAccountID {
		return AccountInfo{}, ErrAccountIDInvalid
	}
	hash, err := accounts.hash(ctx, password)
	if err != nil {
		return AccountInfo{}, err
	}
	now := time.Now().UTC()
	result, err := accounts.db.ExecContext(
		ctx,
		`INSERT INTO accounts (id, password_hash, created_at_ms)
		 SELECT $1, $2, $3
		 WHERE NOT EXISTS (SELECT 1 FROM accounts WHERE lower(id) = lower($4))`,
		accountID, hash, now.UnixMilli(), accountID,
	)
	if err != nil && (strings.Contains(err.Error(), "UNIQUE constraint failed") ||
		strings.Contains(err.Error(), "duplicate key value violates unique constraint")) {
		// Another writer won the race the NOT EXISTS guard could not see.
		return AccountInfo{}, ErrAccountExists
	}
	if err != nil {
		return AccountInfo{}, err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return AccountInfo{}, ErrAccountExists
	}
	return AccountInfo{ID: accountID, CreatedAt: time.UnixMilli(now.UnixMilli()).UTC()}, nil
}

/*
================
Verify

Whether password opens an enabled account. A missing account still pays
one bcrypt comparison so response time does not reveal which ids exist.
================
*/
func (accounts *Accounts) Verify(ctx context.Context, accountID, password string) (bool, error) {
	if len(password) == 0 || len(password) > MaxPasswordBytes {
		return false, nil
	}
	if err := accounts.acquire(ctx); err != nil {
		return false, err
	}
	defer accounts.release()
	hash, found := accounts.PasswordHash(accountID)
	if !found {
		hash = accounts.dummyHash()
	}
	return found && bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil, nil
}

/*
================
SetPassword
================
*/
func (accounts *Accounts) SetPassword(ctx context.Context, accountID, password string) error {
	hash, err := accounts.hash(ctx, password)
	if err != nil {
		return err
	}
	result, err := accounts.db.ExecContext(
		ctx,
		`UPDATE accounts SET password_hash = $1, session_epoch = session_epoch + 1 WHERE id = $2`,
		hash, accountID,
	)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrAccountNotFound
	}
	if epoch, err := accounts.readEpoch(ctx, accountID); err == nil {
		accounts.notifyRevoke(accountID, epoch)
	}
	return nil
}

/*
================
UpgradePasswordHash

Rehashes password at the store's cost only while the stored hash is still
verified, the one the caller checked password against. A password change
that landed in between wins: the swap matches no row and reports false.
================
*/
func (accounts *Accounts) UpgradePasswordHash(ctx context.Context, accountID string, verified []byte, password string) (bool, error) {
	hash, err := accounts.hash(ctx, password)
	if err != nil {
		return false, err
	}
	result, err := accounts.db.ExecContext(
		ctx,
		`UPDATE accounts SET password_hash = $1 WHERE id = $2 AND password_hash = $3`,
		hash, accountID, verified,
	)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

/*
================
SetDisabled
================
*/
func (accounts *Accounts) SetDisabled(ctx context.Context, accountID string, disabled bool) error {
	flag := 0
	if disabled {
		flag = 1
	}
	result, err := accounts.db.ExecContext(
		ctx,
		`UPDATE accounts SET disabled = $1, session_epoch = session_epoch + 1 WHERE id = $2`,
		flag, accountID,
	)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrAccountNotFound
	}
	if epoch, err := accounts.readEpoch(ctx, accountID); err == nil {
		accounts.notifyRevoke(accountID, epoch)
	}
	return nil
}

/*
================
RevokeSessions

Advances the account's session epoch, invalidating all previously minted
sessions and notifying the revocation observer.
================
*/
func (accounts *Accounts) RevokeSessions(ctx context.Context, accountID string) (uint64, error) {
	if !domain.AccountIDValid(accountID) || accountID == domain.ReservedAccountID {
		return 0, ErrAccountIDInvalid
	}
	result, err := accounts.db.ExecContext(
		ctx,
		`UPDATE accounts SET session_epoch = session_epoch + 1 WHERE id = $1`,
		accountID,
	)
	if err != nil {
		return 0, err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return 0, ErrAccountNotFound
	}
	epoch, err := accounts.readEpoch(ctx, accountID)
	if err != nil {
		return 0, err
	}
	accounts.notifyRevoke(accountID, epoch)
	return epoch, nil
}

//============================================================================

/*
================
hash
================
*/
func (accounts *Accounts) hash(ctx context.Context, password string) ([]byte, error) {
	if len(password) < MinPasswordBytes || len(password) > MaxPasswordBytes {
		return nil, ErrPasswordInvalid
	}
	if err := accounts.acquire(ctx); err != nil {
		return nil, err
	}
	defer accounts.release()
	return bcrypt.GenerateFromPassword([]byte(password), accounts.cost)
}

/*
================
BcryptCost

The cost of the hashes this store creates.
================
*/
func (accounts *Accounts) BcryptCost() int {
	return accounts.cost
}

/*
================
acquire
================
*/
func (accounts *Accounts) acquire(ctx context.Context) error {
	select {
	case accounts.hashing <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

/*
================
release
================
*/
func (accounts *Accounts) release() {
	<-accounts.hashing
}

/*
================
dummyHash

A hash at the store's cost that no password matches.
================
*/
func (accounts *Accounts) dummyHash() []byte {
	accounts.dummyOnce.Do(func() {
		accounts.dummy, _ = bcrypt.GenerateFromPassword([]byte("\x00unmatchable\x00"), accounts.cost)
	})
	return accounts.dummy
}
