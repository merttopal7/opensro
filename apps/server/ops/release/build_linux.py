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
	("sro-packer", "./cmd/operations/sro-packer"),
)

BUILD_SUBJECT_SYMBOL = "opensro.online/server/internal/agent/server.BuildSubjectSymbol"

START_SH = """#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "$0")"

chmod +x agent gameworld panel sro-nomad sro-init sro-provision-identity hash-agent-password sro-authority-upgrade sro-game-data-check sro-swap-sun sro-packer init-postgres.sh ops/backup/*.sh

export SRO_SERVER_GAME_DATA_ROOT="$(pwd)/.generated/game-data/1.150/server.srogz"
export SRO_AGENT_POSTGRES_DSN="postgres://app:123@127.0.0.1:5433/opensro?sslmode=disable"
export SRO_PANEL_ADDR="${SRO_PANEL_ADDR:-127.0.0.1:8795}"
export SRO_ALEXANDRIA="${SRO_ALEXANDRIA:-on}"
export SRO_LEVEL_CAP="${SRO_LEVEL_CAP:-110}"
export SRO_CAP110_MONSTERS="${SRO_CAP110_MONSTERS:-on}"
export SRO_PHARAOH_TOMB="${SRO_PHARAOH_TOMB:-on}"
export SRO_PHARAOH_TOMB_GEAR_RATE="${SRO_PHARAOH_TOMB_GEAR_RATE:-5}"
# Browser origins the Agent and GameWorld accept (sro-nomad -allowed-origins),
# comma-separated, e.g. "https://sro.ftemir.com" for a browser client or
# installed PWA served from the server's domain. The environment wins, else
# config/allowed-origins, which lives only on the host (deploys add files and
# never remove one) so it survives this script being regenerated. Empty keeps
# sro-nomad's loopback development list. The desktop shell drops its Origin
# header and needs no entry.
export SRO_ALLOWED_ORIGINS="${SRO_ALLOWED_ORIGINS:-$(if [ -f config/allowed-origins ]; then tr -d '[:space:]' < config/allowed-origins; fi)}"

mkdir -p .state/cluster
echo "$SRO_AGENT_POSTGRES_DSN" > .state/cluster/postgres-dsn.txt

# port_listening PORT: true when something listens on 127.0.0.1/any PORT.
# grep reads all of ss's output (no -q): under pipefail an early-exiting
# `grep -q` kills ss with SIGPIPE and the pipeline reports failure even on a
# match, which made a running Nomad look down and triggered a pkill -9.
port_listening() {
	ss -tln 2>/dev/null | grep -E ":$1([[:space:]]|$)" >/dev/null
}

# wait_gameworlds_stopped: after `job stop`, wait (up to 60 s) until no
# GameWorld allocation is still pending or running. The jobs use static ports
# (transport 8788, control 8791, ...); planning the new jobs while the old
# allocations still hold them fails with "cannot place task groups".
wait_gameworlds_stopped() {
	for _ in $(seq 1 60); do
		busy=0
		for gw_job in $("$SRO_NOMAD_BINARY" status 2>/dev/null | awk '/^sro-gameworld-/{print $1}'); do
			if "$SRO_NOMAD_BINARY" job allocs -json "$gw_job" 2>/dev/null | grep -E '"ClientStatus": ?"(pending|running)"' >/dev/null; then
				busy=1
			fi
		done
		if [ "$busy" -eq 0 ]; then
			return 0
		fi
		sleep 1
	done
	echo "      warning: GameWorld allocations still running after 60 s"
}

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
if command -v docker >/dev/null 2>&1 && ! port_listening 5433; then
	echo "      Starting PostgreSQL via Docker..."
	docker compose up -d
	sleep 3
fi

# 3) Start Nomad dev-agent in background
mkdir -p .state/cluster
if ! port_listening 4646; then
	echo "[3/6] Cleaning orphaned release processes and starting Nomad supervisor..."
	pkill -9 -f "\\.state/cluster/releases/" >/dev/null 2>&1 || true
	sleep 1
	nohup ./sro-nomad dev-agent > .state/nomad-agent.log 2>&1 &
	for _ in $(seq 1 20); do
		if port_listening 4646; then
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
sleep 1

# 6) Check client updates and deploy sro-agent + sro-gameworld
mkdir -p updates
./sro-packer sync -updates "$(pwd)/updates" || true
for gw_job in $("$SRO_NOMAD_BINARY" status 2>/dev/null | awk '/^sro-gameworld-/{print $1}'); do
	"$SRO_NOMAD_BINARY" job stop "$gw_job" >/dev/null 2>&1 || true
done
wait_gameworlds_stopped
for auth_dir in .state/shards/*/authority; do
	if [ -f "$auth_dir/state.db" ]; then
		rm -f "$auth_dir/authority.lock"
		./sro-authority-upgrade -authority-dir "$auth_dir" -commit || true
		rm -f "$auth_dir/authority.lock"
	fi
done
echo "[6/6] Deploying sro-agent and sro-gameworld..."
"$SRO_NOMAD_BINARY" var purge sro/operations/fleet >/dev/null 2>&1 || true
./sro-nomad deploy -agent-cpu=100 -agent-memory-mb=128 -gameworld-cpu=200 -gameworld-memory-mb=1024 \\
	${SRO_ALLOWED_ORIGINS:+-allowed-origins="$SRO_ALLOWED_ORIGINS"}

echo ""
echo "=== OpenSRO Server Active! ==="
echo "Agent API     : http://127.0.0.1:8787"
echo "Client Updates: http://127.0.0.1:8787/updates/patches.json"
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


DESKTOP_INDEX_HTML_TEMPLATE = """<!doctype html>
<html lang="en">
\t<head>
\t\t<meta charset="utf-8" />
\t\t<meta name="viewport" content="width=device-width,initial-scale=1" />
\t\t<link rel="icon" href="data:," />
\t\t<title>Silkroad Online · New Silkroad</title>
\t\t<meta name="theme-color" content="#101820" />
\t\t<link rel="stylesheet" crossorigin href="/assets/{css_file}" />
\t\t<link rel="stylesheet" href="/styles.css" />
\t\t<script type="module" src="/main.js"></script>
\t\t<script type="module" crossorigin src="/assets/{js_file}"></script>
\t</head>
\t<body>
\t\t<header id="top-drag-bar" class="drag-bar" title="Pencereyi sürüklemek için basılı tutun"></header>
\t\t<div id="startup-loading" class="sro-boot-loading" data-active="true" role="status" aria-live="polite">
\t\t\t<div class="sro-boot-loading__inner">
\t\t\t\t<img
\t\t\t\t\tclass="sro-boot-loading__logo"
\t\t\t\t\tsrc="/assets/images/Media_extracted/interface/outer/logo-big.png"
\t\t\t\t\twidth="870"
\t\t\t\t\theight="320"
\t\t\t\t\talt="Silkroad Online"
\t\t\t\t/>
\t\t\t\t<img
\t\t\t\t\tclass="sro-boot-loading__native-label"
\t\t\t\t\tsrc="/assets/images/Media_extracted/interface/loading/nowloading.png"
\t\t\t\t\twidth="144"
\t\t\t\t\theight="20"
\t\t\t\t\talt="Now loading"
\t\t\t\t/>
\t\t\t\t<div class="sro-boot-loading__copy">
\t\t\t\t\t<div class="sro-boot-loading__label">Preparing your journey</div>
\t\t\t\t\t<div class="sro-boot-loading__detail">Loading local game files</div>
\t\t\t\t</div>
\t\t\t\t<div class="sro-boot-loading__bar">
\t\t\t\t\t<div class="sro-boot-loading__bar-track"><div class="sro-boot-loading__bar-fill"></div></div>
\t\t\t\t</div>
\t\t\t</div>
\t\t</div>
\t\t<canvas aria-label="Silkroad replacement renderer"></canvas>
\t\t<div id="fps-chip" class="sro-fps-chip" data-expanded="false">
\t\t\t<button
\t\t\t\tid="fps-toggle"
\t\t\t\tclass="sro-fps-chip__toggle"
\t\t\t\ttype="button"
\t\t\t\taria-expanded="false"
\t\t\t\taria-controls="fps-readout"
\t\t\t\taria-label="Show FPS telemetry"
\t\t\t\ttitle="Show FPS telemetry"
\t\t\t>
\t\t\t\tF
\t\t\t</button>
\t\t\t<div id="fps-readout" class="sro-fps-chip__readout" role="status" aria-live="off" hidden></div>
\t\t</div>
\t\t<div id="update-notice" class="sro-update-notice" role="status" hidden>
\t\t\t<span>Yeni bir oyun güncellemesi yayınlandı. İstemci 5 saniye içinde kapatılıyor…</span>
\t\t\t<button type="button">Hemen Kapat</button>
\t\t</div>
\t\t<section id="loading-transfer" class="loading-transfer" hidden aria-label="Loading progress">
\t\t\t<div class="loading-transfer__header">
\t\t\t\t<span class="loading-transfer__pulse" aria-hidden="true"></span>
\t\t\t\t<span data-transfer="title">Preparing your journey</span>
\t\t\t</div>
\t\t\t<div data-transfer="file" class="loading-transfer__file" aria-live="polite">Discovering game files</div>
\t\t\t<dl class="loading-transfer__metrics">
\t\t\t\t<div>
\t\t\t\t\t<dt>Data loaded</dt>
\t\t\t\t\t<dd data-transfer="bytes">0 MB</dd>
\t\t\t\t</div>
\t\t\t\t<div>
\t\t\t\t\t<dt>Transfer rate</dt>
\t\t\t\t\t<dd data-transfer="speed">—</dd>
\t\t\t\t</div>
\t\t\t\t<div>
\t\t\t\t\t<dt>Files ready</dt>
\t\t\t\t\t<dd data-transfer="files">0</dd>
\t\t\t\t</div>
\t\t\t\t<div>
\t\t\t\t\t<dt>Cache reused</dt>
\t\t\t\t\t<dd data-transfer="cache">0</dd>
\t\t\t\t</div>
\t\t\t</dl>
\t\t\t<div data-transfer="queue" class="loading-transfer__queue">Discovering what this scene needs</div>
\t\t</section>
\t\t<output aria-hidden="true">Starting replacement runtime…</output>
\t</body>
</html>
"""


# ================
# prepare_client_dist
# ================
def prepare_client_dist(src_dir: Path, dst_dir: Path) -> None:
	if dst_dir.exists():
		shutil.rmtree(dst_dir)
	shutil.copytree(src_dir, dst_dir)
	viewer = dst_dir / "viewer.html"
	if viewer.is_file():
		viewer.unlink()
	for cand_parent in (src_dir.parents[5], src_dir.parents[4]):
		desktop_src = cand_parent / "new-silkroad" / "src"
		if desktop_src.is_dir():
			for extra_name in ("main.js", "styles.css"):
				extra_src = desktop_src / extra_name
				if extra_src.is_file():
					shutil.copy2(extra_src, dst_dir / extra_name)
			break
	repo_root = src_dir.parents[4]
	scripts_dir = str(repo_root / "scripts")
	if scripts_dir not in sys.path:
		sys.path.insert(0, scripts_dir)
	from sro_paths import PUBLIC_ROOT

	cursors_src = PUBLIC_ROOT / "assets" / "cursors"
	if cursors_src.is_dir():
		cursors_dst = dst_dir / "assets" / "cursors"
		if cursors_dst.exists():
			shutil.rmtree(cursors_dst)
		shutil.copytree(cursors_src, cursors_dst)
	assets_dir = dst_dir / "assets"
	if assets_dir.is_dir():
		js_files = sorted(f.name for f in assets_dir.glob("bootstrap-*.js"))
		css_files = sorted(f.name for f in assets_dir.glob("index-*.css"))
		if js_files and css_files:
			rendered = DESKTOP_INDEX_HTML_TEMPLATE.format(
				js_file=js_files[-1],
				css_file=css_files[-1],
			)
			(dst_dir / "index.html").write_bytes(rendered.encode("utf-8"))

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

	client_dist_src = module_root.parent / "client-next" / "temp" / "artifacts" / "dist"
	client_dist_dst = out_dir / "client-dist"
	if client_dist_src.is_dir():
		prepare_client_dist(client_dist_src, client_dist_dst)
		print(f"[linux/{arch}] copied client dist -> {client_dist_dst}")

	last_version_dst = out_dir / "last_version"
	last_version_dst.mkdir(parents=True, exist_ok=True)
	updates_dst = out_dir / "updates"
	updates_dst.mkdir(parents=True, exist_ok=True)
	baseline_dst = updates_dst / "baseline.json"
	if not baseline_dst.is_file():
		subprocess.run(
			[
				go_bin,
				"run",
				"./cmd/operations/sro-packer",
				"baseline",
				"-updates",
				str(updates_dst),
				"-dist",
				str(client_dist_dst),
			],
			cwd=module_root,
			check=False,
		)
	subprocess.run(
		[
			go_bin,
			"run",
			"./cmd/operations/sro-packer",
			"sync",
			"-updates",
			str(updates_dst),
			"-dist",
			str(client_dist_dst),
		],
		cwd=module_root,
		check=False,
	)

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
