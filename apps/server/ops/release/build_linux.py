#!/usr/bin/env python3
"""
===========================================================================

build_linux.py - build a complete, self-contained Linux server folder

Cross-compiles all pure-Go Linux binaries (CGO_ENABLED=0, GOOS=linux) and
assembles one self-contained deployment folder containing binaries, Nomad
jobs/configs, backup scripts, and the 78.7 MB server.srogz game-data archive.

===========================================================================
"""

import argparse
import base64
import os
from pathlib import Path
import shutil
import subprocess
import sys

TARGETS = (
	("agent", "./cmd/services/sro-agent"),
	("gameworld", "./cmd/services/sro-gameworld"),
	("panel", "./cmd/services/sro-panel"),
	("sro-nomad", "./cmd/operations/sro-nomad"),
	("sro-init", "./cmd/operations/sro-init"),
	("sro-provision-identity", "./cmd/operations/sro-provision-identity"),
	("hash-agent-password", "./cmd/operations/hash-agent-password"),
	("sro-authority-upgrade", "./cmd/tools/sro-authority-upgrade"),
	("sro-game-data-check", "./cmd/tools/sro-game-data-check"),
	("sro-swap-sun", "./cmd/operations/sro-swap-sun"),
)

BUILD_SUBJECT_SYMBOL = "opensro.online/server/internal/agent/server.BuildSubjectSymbol"

START_SH = """#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "$0")"

chmod +x agent gameworld panel sro-nomad sro-init sro-provision-identity hash-agent-password sro-authority-upgrade sro-game-data-check sro-swap-sun init-postgres.sh ops/backup/*.sh

export SRO_SERVER_GAME_DATA_ROOT="$(pwd)/.generated/game-data/1.150/server.srogz"
export SRO_AGENT_POSTGRES_DSN="postgres://app:123@127.0.0.1:5433/opensro?sslmode=disable"
export SRO_PANEL_ADDR="${SRO_PANEL_ADDR:-127.0.0.1:8795}"

mkdir -p .state/cluster
echo "$SRO_AGENT_POSTGRES_DSN" > .state/cluster/postgres-dsn.txt

# 1) Nomad 2.0.7 binary check (downloads automatically if missing)
if [ ! -x "./nomad" ]; then
	if command -v nomad >/dev/null 2>&1; then
		export SRO_NOMAD_BINARY="$(command -v nomad)"
	else
		echo "[1/6] Downloading HashiCorp Nomad 2.0.7..."
		curl -fsSL "https://releases.hashicorp.com/nomad/2.0.7/nomad_2.0.7_linux_amd64.zip" -o nomad.zip
		unzip -o nomad.zip nomad
		rm -f nomad.zip
		chmod +x ./nomad
		export SRO_NOMAD_BINARY="$(pwd)/nomad"
	fi
else
	export SRO_NOMAD_BINARY="$(pwd)/nomad"
fi

# 2) PostgreSQL check
echo "[2/6] Checking PostgreSQL ($SRO_AGENT_POSTGRES_DSN)..."
if command -v docker >/dev/null 2>&1 && ! ss -tln | grep -Eq ':5433([[:space:]]|$)'; then
	echo "      Starting PostgreSQL via Docker..."
	docker compose up -d
	sleep 3
fi

# 3) Start Nomad dev-agent in background
mkdir -p .state/cluster
if ! ss -tln | grep -Eq ':4646([[:space:]]|$)'; then
	echo "[3/6] Cleaning orphaned release processes and starting Nomad supervisor..."
	pkill -9 -f "\\.state/cluster/releases/" >/dev/null 2>&1 || true
	sleep 1
	nohup ./sro-nomad dev-agent > .state/nomad-agent.log 2>&1 &
	for _ in $(seq 1 20); do
		if ss -tln | grep -Eq ':4646([[:space:]]|$)'; then
			sleep 3
			break
		fi
		sleep 1
	done
else
	echo "[3/6] Nomad supervisor already running (127.0.0.1:4646)."
fi

# 4) Initial identity keys and global-official shard setup
if [ ! -f ".state/cluster/agent-provisioning-token" ]; then
	echo "[4/6] Provisioning identity keys and shard database..."
	./sro-provision-identity
	./sro-init -shard global-official
else
	echo "[4/6] Identity keys already exist, preserving."
fi
if [ ! -f ".state/cluster/accounts.json" ] || [ "$(cat .state/cluster/accounts.json 2>/dev/null)" = "[]" ]; then
	echo '[{"id":"tester","passwordHash":"$2a$10$58hJQO.3HVzQLXaLFBDwSuXRiTuvUjf/oUJBxKJEKJX.mO5shR1zm"}]' > .state/cluster/accounts.json
fi

# 5) Start Customer & Shard Management Panel (sro-panel)
echo "[5/6] Starting Customer & Shard Panel ($SRO_PANEL_ADDR)..."
pkill -x panel >/dev/null 2>&1 || true
nohup ./panel > .state/panel.log 2>&1 &

# 6) Deploy sro-agent and sro-gameworld
echo "[6/6] Deploying sro-agent and sro-gameworld..."
"$SRO_NOMAD_BINARY" var purge sro/operations/fleet >/dev/null 2>&1 || true
./sro-nomad deploy -agent-cpu=100 -agent-memory-mb=128 -gameworld-cpu=200 -gameworld-memory-mb=1024

echo ""
echo "=== OpenSRO Server Active! ==="
echo "Agent API     : http://127.0.0.1:8787"
echo "GameWorld     : http://127.0.0.1:8788"
echo "Customer Panel: http://127.0.0.1:8795"
echo "Status        : ./sro-nomad status"
"""

DOCKER_COMPOSE_YML = """services:
  postgres:
    image: postgres:16
    container_name: opensro-postgres
    restart: unless-stopped
    environment:
      POSTGRES_USER: app
      POSTGRES_PASSWORD: "123"
      POSTGRES_DB: opensro
    ports:
      - "127.0.0.1:5433:5432"
    volumes:
      - ./postgres-data:/var/lib/postgresql/data
      - ./schema.sql:/docker-entrypoint-initdb.d/01-schema.sql:ro
"""

SCHEMA_SQL = """-- ===========================================================================
-- schema.sql - OpenSRO Central PostgreSQL 16 Schema (accounts, shards, GM)
-- Note: sro-agent also creates/migrates these tables automatically on startup.
-- ===========================================================================

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
	owner_account_id     TEXT     NOT NULL DEFAULT '',
	check_mode           SMALLINT NOT NULL DEFAULT 0 CHECK (check_mode IN (0, 1)),
	max_level            INTEGER  NOT NULL DEFAULT 90 CHECK (max_level >= 1 AND max_level <= 140),
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

CREATE TABLE IF NOT EXISTS shard_bans (
	shard_id     TEXT   NOT NULL,
	account_id   TEXT   NOT NULL,
	reason       TEXT   NOT NULL DEFAULT '',
	banned_at_ms BIGINT NOT NULL,
	PRIMARY KEY (shard_id, account_id)
);
"""

INIT_POSTGRES_SH = """#!/usr/bin/env bash
# Creates the 'app' role, 'opensro' database, and applies schema.sql on native Linux PostgreSQL.
set -Eeuo pipefail
cd "$(dirname "$0")"

sudo -u postgres psql -v ON_ERROR_STOP=1 <<'EOSQL'
DO $$
BEGIN
	IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'app') THEN
		CREATE ROLE app WITH LOGIN PASSWORD '123';
	END IF;
END
$$;
SELECT 'CREATE DATABASE opensro OWNER app'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'opensro')\\gexec
EOSQL

sudo -u postgres psql -v ON_ERROR_STOP=1 -d opensro -f schema.sql
sudo -u postgres psql -v ON_ERROR_STOP=1 -d opensro <<'EOSQL'
ALTER SCHEMA public OWNER TO app;
GRANT ALL ON SCHEMA public TO app;
ALTER TABLE accounts OWNER TO app;
ALTER TABLE shards OWNER TO app;
ALTER TABLE account_gm_shards OWNER TO app;
ALTER TABLE shard_bans OWNER TO app;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO app;
EOSQL
echo "PostgreSQL 'opensro' veritabani ve tablolar hazir!"
"""


# ================
# resolve_go_binary
# ================
def resolve_go_binary(repo_root: Path) -> str:
	local_win = repo_root / ".tools" / "go" / "bin" / "go.exe"
	if local_win.is_file():
		return str(local_win)
	local_unix = repo_root / ".tools" / "go" / "bin" / "go"
	if local_unix.is_file():
		return str(local_unix)
	found = shutil.which("go")
	if found:
		return found
	raise FileNotFoundError("Go toolchain not found in .tools/go/bin or PATH")


# ================
# agent_ldflags
# ================
def agent_ldflags(module_root: Path) -> str:
	try:
		subject = subprocess.check_output(
			["git", "log", "-1", "--format=%s"],
			cwd=module_root,
			stderr=subprocess.DEVNULL,
		).strip()
	except Exception:
		subject = b""
	encoded = base64.b64encode(subject).decode("ascii")
	return f"-s -w -X {BUILD_SUBJECT_SYMBOL}={encoded}"


# ================
# assemble_package
# ================
def assemble_package(go_bin: str, module_root: Path, out_dir: Path, arch: str) -> None:
	out_dir.mkdir(parents=True, exist_ok=True)
	env = os.environ.copy()
	env["CGO_ENABLED"] = "0"
	env["GOOS"] = "linux"
	env["GOARCH"] = arch

	for name, pkg in TARGETS:
		dest = out_dir / name
		ldflags = agent_ldflags(module_root) if name == "agent" else "-s -w"
		cmd = [
			go_bin,
			"build",
			"-trimpath",
			f"-ldflags={ldflags}",
			"-o",
			str(dest),
			pkg,
		]
		print(f"[linux/{arch}] building {name} -> {dest}")
		subprocess.run(cmd, cwd=module_root, env=env, check=True)

	# Copy required runtime files into the single folder
	shutil.copy2(module_root / "go.mod", out_dir / "go.mod")

	config_dir = out_dir / "config"
	config_dir.mkdir(parents=True, exist_ok=True)
	shutil.copy2(module_root / "config" / "shards.json", config_dir / "shards.json")

	for rel_dir in (
		Path("ops/nomad/jobs"),
		Path("ops/nomad/config"),
		Path("ops/nomad/access"),
		Path("ops/backup"),
	):
		src_dir = module_root / rel_dir
		dst_dir = out_dir / rel_dir
		if dst_dir.exists():
			shutil.rmtree(dst_dir)
		shutil.copytree(src_dir, dst_dir)

	srogz_src = module_root / ".generated" / "game-data" / "1.150" / "server.srogz"
	if srogz_src.is_file():
		srogz_dst = out_dir / ".generated" / "game-data" / "1.150" / "server.srogz"
		srogz_dst.parent.mkdir(parents=True, exist_ok=True)
		shutil.copy2(srogz_src, srogz_dst)
		print(f"[linux/{arch}] copied game data -> {srogz_dst} ({srogz_dst.stat().st_size // (1024 * 1024)} MB)")

	start_script = out_dir / "start-linux.sh"
	start_script.write_bytes(START_SH.encode("utf-8"))
	(out_dir / "docker-compose.yml").write_bytes(DOCKER_COMPOSE_YML.encode("utf-8"))
	(out_dir / "schema.sql").write_bytes(SCHEMA_SQL.encode("utf-8"))
	(out_dir / "init-postgres.sh").write_bytes(INIT_POSTGRES_SH.encode("utf-8"))


# ================
# main
# ================
def main() -> int:
	parser = argparse.ArgumentParser(description="Build self-contained OpenSRO Linux server folder")
	parser.add_argument(
		"--arch",
		default="amd64",
		choices=("amd64", "arm64"),
		help="Target Linux architecture (default: amd64)",
	)
	parser.add_argument(
		"--output",
		type=Path,
		default=None,
		help="Output folder (default: ../linux-server)",
	)
	args = parser.parse_args()

	module_root = Path(__file__).resolve().parents[2]
	repo_root = module_root.parent.parent
	go_bin = resolve_go_binary(repo_root)
	out_dir = args.output if args.output else (repo_root.parent / "linux-server")

	assemble_package(go_bin, module_root, out_dir, args.arch)
	print(f"Self-contained Linux server folder ready at: {out_dir}")
	return 0


if __name__ == "__main__":
	sys.exit(main())
