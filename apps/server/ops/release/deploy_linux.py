#!/usr/bin/env python3
"""
===========================================================================

deploy_linux.py - build and deploy linux-server changes over SSH or SFTP

Builds the Linux server release folder (including compiled Go binaries,
client-dist, and differential updates/PatchNNN.temir + updates/patches.json),
computes SHA-256 checksums against the remote server so only modified or new
files are transmitted, uploads them via SSH streaming tar or SFTP batch put,
and triggers a remote hot-redeploy of sro-agent, sro-gameworld, and sro-panel.

===========================================================================
"""

import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile

SKIP_LOCAL_NAMES = {
	".state",
	"postgres-data",
	"linux-server.zip",
	"deploy.json",
	"deploy.local.json",
	"deploy.ps1",
	# updates/ cross-process sync lock (internal/agent/updates); host-local
	".sync.lock",
}

EXECUTABLE_NAMES = {
	"agent",
	"gameworld",
	"panel",
	"sro-nomad",
	"sro-init",
	"sro-provision-identity",
	"hash-agent-password",
	"sro-authority-upgrade",
	"sro-game-data-check",
	"sro-swap-sun",
	"sro-packer",
	"start-linux.sh",
	"init-postgres.sh",
}


# ================
# sha256_file
# ================
def sha256_file(path: Path) -> str:
	h = hashlib.sha256()
	with path.open("rb") as f:
		while True:
			chunk = f.read(256 * 1024)
			if not chunk:
				break
			h.update(chunk)
	return h.hexdigest()


# ================
# collect_deploy_files
# ================
def collect_deploy_files(local_dir: Path, only_updates: bool) -> list[tuple[str, Path]]:
	results: list[tuple[str, Path]] = []
	for root, dirs, files in os.walk(local_dir):
		rel_root = Path(root).relative_to(local_dir)
		dirs[:] = sorted(
			d for d in dirs
			if d not in SKIP_LOCAL_NAMES and not d.startswith(".git")
		)
		for fname in sorted(files):
			if fname in SKIP_LOCAL_NAMES or fname.endswith(".log") or fname.endswith(".tmp"):
				continue
			full_path = Path(root) / fname
			rel_posix = (rel_root / fname).as_posix() if str(rel_root) != "." else fname
			if only_updates:
				if not (
					rel_posix.startswith("updates/")
					or rel_posix.startswith("last_version/")
					or rel_posix.startswith("client-dist/")
				):
					continue
			results.append((rel_posix, full_path))
	return results


# ================
# load_deploy_config
# ================
def load_deploy_config(config_path: Path | None, repo_root: Path, local_dir: Path) -> dict:
	candidates = []
	if config_path:
		candidates.append(config_path)
	candidates.extend([
		repo_root / "deploy.local.json",
		repo_root / "apps" / "server" / "ops" / "release" / "deploy.local.json",
		local_dir / "deploy.local.json",
		local_dir / "deploy.json",
	])
	for cand in candidates:
		if cand.is_file():
			try:
				return json.loads(cand.read_text(encoding="utf-8"))
			except Exception as exc:
				print(f"[uyari] {cand} okunamadi: {exc}", file=sys.stderr)
	return {}


# ================
# create_askpass_helper
# ================
def create_askpass_helper(password: str) -> tuple[ dict[str, str], Path | None ]:
	env = os.environ.copy()
	if not password:
		return env, None

	tmp_dir = Path(tempfile.mkdtemp(prefix="sro_ssh_"))
	env["SRO_SSH_PASS_SECRET"] = password
	env["DISPLAY"] = "1"
	env["SSH_ASKPASS_REQUIRE"] = "force"

	if os.name == "nt":
		helper = tmp_dir / "askpass.cmd"
		helper.write_bytes(b"@echo off\r\necho %SRO_SSH_PASS_SECRET%\r\n")
	else:
		helper = tmp_dir / "askpass.sh"
		helper.write_bytes(b"#!/bin/sh\necho \"$SRO_SSH_PASS_SECRET\"\n")
		helper.chmod(0o700)

	env["SSH_ASKPASS"] = str(helper)
	return env, tmp_dir


# ================
# ssh_base_args
# ================
def ssh_base_args(port: int, key_file: str, is_scp_or_sftp: bool = False) -> list[str]:
	args = [
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=15",
	]
	if is_scp_or_sftp:
		args.extend(["-P", str(port)])
	else:
		args.extend(["-p", str(port)])
	if key_file:
		args.extend(["-i", key_file])
	return args


def sudo_echo_prefix(target: str, password: str) -> str:
	if target.startswith("root@") or not password:
		return ""
	escaped = password.replace("'", "'\\''")
	return f"printf '%s\\n' '{escaped}' | sudo -S "


# ================
# fetch_remote_hashes
# ================
def fetch_remote_hashes(
	target: str,
	port: int,
	key_file: str,
	remote_dir: str,
	password: str,
	env: dict[str, str],
) -> dict[str, str]:
	sudo_prefix = sudo_echo_prefix(target, password)
	remote_cmd = (
		f"{sudo_prefix}mkdir -p '{remote_dir}/updates' '{remote_dir}/config' && "
		f"cd '{remote_dir}' && "
		f"{sudo_prefix}find . -maxdepth 6 -type f "
		"! -path './.state/*' ! -path './postgres-data/*' "
		"-exec sha256sum {} + 2>/dev/null || true"
	)
	cmd = ["ssh", "-n", *ssh_base_args(port, key_file, False), target, remote_cmd]
	proc = subprocess.run(cmd, stdin=subprocess.DEVNULL, env=env, capture_output=True, text=True)
	if proc.returncode != 0:
		raise RuntimeError(
			f"SSH baglantisi basarisiz ({target}:{port}):\n{proc.stderr.strip()}"
		)
	hashes: dict[str, str] = {}
	for line in proc.stdout.splitlines():
		parts = line.strip().split(None, 1)
		if len(parts) == 2 and len(parts[0]) == 64:
			digest, rel = parts
			clean_rel = rel.lstrip("./")
			hashes[clean_rel] = digest.lower()
	return hashes


# ================
# deploy_via_ssh_tar
# ================
def deploy_via_ssh_tar(
	target: str,
	port: int,
	key_file: str,
	remote_dir: str,
	changed: list[tuple[str, Path]],
	password: str,
	env: dict[str, str],
) -> None:
	sudo_prefix = sudo_echo_prefix(target, password)
	small_files: list[tuple[str, Path]] = []
	large_files: list[tuple[str, Path]] = []
	for rel_posix, disk_path in changed:
		if disk_path.stat().st_size > 32 * 1024 * 1024 or rel_posix.endswith(".temir"):
			large_files.append((rel_posix, disk_path))
		else:
			small_files.append((rel_posix, disk_path))

	for idx, (rel_posix, disk_path) in enumerate(large_files, 1):
		mb = disk_path.stat().st_size / (1024.0 * 1024.0)
		print(f"[deploy] [{idx}/{len(large_files)}] Buyuk dosya aktariliyor: {rel_posix} ({mb:.2f} MB)...")
		tmp_remote = f"/tmp/opensro_large_{os.getpid()}_{idx}.bin"
		scp_cmd = ["scp", *ssh_base_args(port, key_file, True), str(disk_path), f"{target}:{tmp_remote}"]
		proc = subprocess.run(scp_cmd, stdin=subprocess.DEVNULL, env=env)
		if proc.returncode != 0:
			raise RuntimeError(f"SCP ile {rel_posix} aktarimi basarisiz oldu.")

		remote_dest = f"{remote_dir}/{rel_posix}"
		remote_parent = remote_dest.rsplit("/", 1)[0]
		mv_remote = (
			f"{sudo_prefix}sh -c \"mkdir -p '{remote_parent}' && "
			f"mv -f '{tmp_remote}' '{remote_dest}' && "
			f"chmod 644 '{remote_dest}'\""
		)
		proc2 = subprocess.run(
			["ssh", "-n", *ssh_base_args(port, key_file, False), target, mv_remote],
			stdin=subprocess.DEVNULL,
			env=env,
		)
		if proc2.returncode != 0:
			raise RuntimeError(f"Uzak sunucuda {rel_posix} tasima islemi basarisiz oldu.")

	if small_files:
		buf = io.BytesIO()
		with tarfile.open(fileobj=buf, mode="w:gz") as tf:
			for rel_posix, disk_path in small_files:
				info = tf.gettarinfo(str(disk_path), arcname=rel_posix)
				base_name = rel_posix.rsplit("/", 1)[-1]
				if base_name in EXECUTABLE_NAMES or rel_posix.endswith(".sh"):
					info.mode = 0o755
				else:
					info.mode = 0o644
				with disk_path.open("rb") as f:
					tf.addfile(info, f)

		payload = buf.getvalue()
		mb = len(payload) / (1024.0 * 1024.0)
		print(f"[deploy] {len(small_files)} dosya paketlendi ({mb:.2f} MB sikistirilmis), SSH uzerinden aktariliyor...")

		tmp_archive = f"/tmp/opensro_deploy_{os.getpid()}.tar.gz"
		upload_cmd = ["ssh", *ssh_base_args(port, key_file, False), target, f"cat > '{tmp_archive}'"]
		proc = subprocess.run(upload_cmd, input=payload, env=env)
		if proc.returncode != 0:
			raise RuntimeError("SSH uzerinden arsiv yuklemesi basarisiz oldu.")

		extract_remote = (
			f"{sudo_prefix}sh -c \"mkdir -p '{remote_dir}' && "
			f"tar -xzf '{tmp_archive}' -C '{remote_dir}' && "
			f"rm -f '{tmp_archive}'\""
		)
		proc2 = subprocess.run(
			["ssh", "-n", *ssh_base_args(port, key_file, False), target, extract_remote],
			stdin=subprocess.DEVNULL,
			env=env,
		)
		if proc2.returncode != 0:
			raise RuntimeError("SSH uzerinden arsiv acma islemi basarisiz oldu.")


# ================
# deploy_via_sftp
# ================
def deploy_via_sftp(
	target: str,
	port: int,
	key_file: str,
	remote_dir: str,
	changed: list[tuple[str, Path]],
	env: dict[str, str],
) -> None:
	dirs_to_make: set[str] = set()
	for rel_posix, _ in changed:
		parts = rel_posix.split("/")[:-1]
		cur = ""
		for p in parts:
			cur = f"{cur}/{p}" if cur else p
			dirs_to_make.add(cur)

	batch_lines = [f"-mkdir {remote_dir}"]
	for d in sorted(dirs_to_make):
		batch_lines.append(f"-mkdir {remote_dir}/{d}")
	for rel_posix, disk_path in changed:
		local_escaped = str(disk_path).replace("\\", "/")
		batch_lines.append(f'put "{local_escaped}" "{remote_dir}/{rel_posix}"')
		base_name = rel_posix.rsplit("/", 1)[-1]
		if base_name in EXECUTABLE_NAMES or rel_posix.endswith(".sh"):
			batch_lines.append(f"chmod 755 \"{remote_dir}/{rel_posix}\"")

	with tempfile.NamedTemporaryFile("w", suffix=".sftp", delete=False, encoding="utf-8") as bf:
		bf.write("\n".join(batch_lines) + "\n")
		batch_path = Path(bf.name)

	try:
		print(f"[deploy] {len(changed)} dosya SFTP ile '{target}:{remote_dir}' dizinine yukleniyor...")
		cmd = ["sftp", *ssh_base_args(port, key_file, True), "-b", str(batch_path), target]
		proc = subprocess.run(cmd, env=env)
		if proc.returncode != 0:
			raise RuntimeError("SFTP aktarimi basarisiz oldu.")
	finally:
		batch_path.unlink(missing_ok=True)


# ================
# restart_remote_services
# ================
def restart_remote_services(
	target: str,
	port: int,
	key_file: str,
	remote_dir: str,
	only_updates: bool,
	password: str,
	env: dict[str, str],
) -> None:
	sudo_prefix = sudo_echo_prefix(target, password)
	if only_updates:
		remote_cmd = (
			f"{sudo_prefix}sh -c \"cd '{remote_dir}' && "
			"./sro-packer sync -updates updates && "
			"echo '[deploy] Istemci guncellemeleri (updates/patches.json) hazir!'\""
		)
	else:
		remote_cmd = f"{sudo_prefix}sh -c \"cd '{remote_dir}' && chmod +x start-linux.sh && ./start-linux.sh\""

	print(f"[deploy] Uzak sunucu servisleri guncelleniyor ({target})...")
	cmd = ["ssh", "-n", *ssh_base_args(port, key_file, False), target, remote_cmd]
	proc = subprocess.run(cmd, stdin=subprocess.DEVNULL, env=env)
	if proc.returncode != 0:
		raise RuntimeError("Uzak sunucu servis baslatma komutu basarisiz oldu.")


# ================
# main
# ================
def main() -> int:
	parser = argparse.ArgumentParser(
		description="Build and deploy OpenSRO linux-server to a remote Linux host via SSH or SFTP"
	)
	parser.add_argument("--config", type=Path, default=None, help="Path to deploy.json / deploy.local.json")
	parser.add_argument("--host", default=None, help="Remote server hostname or IP (e.g. sro.ftemir.com)")
	parser.add_argument("--port", type=int, default=None, help="SSH/SFTP port (default: 22)")
	parser.add_argument("--user", default=None, help="SSH/SFTP username (default: root)")
	parser.add_argument("--password", default=None, help="SSH/SFTP password (or use SSH key)")
	parser.add_argument("--key", default=None, help="Path to SSH private key (e.g. ~/.ssh/id_ed25519)")
	parser.add_argument("--remote-dir", default=None, help="Remote linux-server directory (default: /root/linux-server)")
	parser.add_argument("--local-dir", type=Path, default=None, help="Local linux-server folder (default: ../linux-server)")
	parser.add_argument("--mode", choices=("ssh", "sftp"), default=None, help="Transfer mode: ssh (fast tar delta) or sftp")
	parser.add_argument("--build", action="store_true", help="Run build_linux.py before deploying")
	parser.add_argument("--no-build", action="store_true", help="Skip running build_linux.py before deploying")
	parser.add_argument("--only-updates", action="store_true", help="Only upload updates/ and client-dist/ (fast client patch push)")
	parser.add_argument("--force-all", action="store_true", help="Upload all files without checking remote SHA-256 hashes")
	parser.add_argument("--no-restart", action="store_true", help="Upload files without running start-linux.sh on the remote server")
	parser.add_argument("--dry-run", action="store_true", help="Show which files would be built and uploaded without connecting")
	args = parser.parse_args()

	script_dir = Path(__file__).resolve().parent
	module_root = script_dir.parents[1]
	repo_root = module_root.parent.parent
	local_dir = args.local_dir if args.local_dir else (repo_root.parent / "linux-server")

	cfg = load_deploy_config(args.config, repo_root, local_dir)

	host = args.host or os.environ.get("SRO_DEPLOY_HOST") or cfg.get("host", "")
	port = args.port or int(os.environ.get("SRO_DEPLOY_PORT", "0")) or int(cfg.get("port", 22))
	user = args.user or os.environ.get("SRO_DEPLOY_USER") or cfg.get("user", "root")
	password = args.password or os.environ.get("SRO_DEPLOY_PASSWORD") or cfg.get("password", "")
	key_file = args.key or os.environ.get("SRO_DEPLOY_KEY") or cfg.get("keyFile", "")
	remote_dir = args.remote_dir or os.environ.get("SRO_DEPLOY_REMOTE_DIR") or cfg.get("remoteDir", "/root/linux-server")
	mode = args.mode or os.environ.get("SRO_DEPLOY_MODE") or cfg.get("mode", "ssh")

	should_build = cfg.get("buildBeforeDeploy", True)
	if args.build:
		should_build = True
	if args.no_build:
		should_build = False

	should_restart = cfg.get("restartServices", True)
	if args.no_restart:
		should_restart = False

	if should_build:
		print("===========================================================================")
		print("[1/3] linux-server derleniyor ve istemci yamalari (updates/) hazirlaniyor...")
		print("===========================================================================")
		build_script = script_dir / "build_linux.py"
		subprocess.run([sys.executable, str(build_script), "--output", str(local_dir)], check=True)

	if not local_dir.is_dir():
		print(f"HATA: Yerel linux-server klasoru bulunamadi: {local_dir}", file=sys.stderr)
		return 1

	candidates = collect_deploy_files(local_dir, args.only_updates)
	if args.dry_run:
		print(f"[dry-run] Toplam kontrol edilen yerel dosya: {len(candidates)}")
		for rel_posix, p in candidates:
			if rel_posix.startswith("updates/") or "/" not in rel_posix:
				print(f"  - {rel_posix} ({p.stat().st_size} bayt)")
		return 0

	if not host:
		print(
			"HATA: Sunucu adresi (host) belirtilmedi.\n"
			f"Lutfen '{local_dir / 'deploy.json'}' dosyasini doldurun veya --host <IP/AlanAdi> parametresi verin.",
			file=sys.stderr,
		)
		return 1

	target = f"{user}@{host}" if user else host
	env, askpass_tmp = create_askpass_helper(password)
	try:
		print("===========================================================================")
		print(f"[2/3] Uzak sunucu kontrol ediliyor: {target}:{port} -> {remote_dir} (mod: {mode})")
		print("===========================================================================")

		remote_hashes: dict[str, str] = {}
		if mode == "ssh" and not args.force_all:
			remote_hashes = fetch_remote_hashes(target, port, key_file, remote_dir, password, env)

		has_local_patches = any(
			rel.startswith("updates/Patch") and rel.endswith(".temir")
			for rel, _ in candidates
		)
		changed: list[tuple[str, Path]] = []
		for rel_posix, disk_path in candidates:
			if (
				rel_posix in ("updates/patches.json", "updates/baseline.json", "config/shards.json")
				and rel_posix in remote_hashes
				and not args.force_all
			):
				# Remote server maintains updates/patches.json and updates/baseline.json
				# via sro-packer sync; never overwrite them with an older local copy.
				continue
			if (
				rel_posix.startswith("last_version/")
				and rel_posix.endswith(".temir")
				and has_local_patches
				and (
					rel_posix in remote_hashes
					or f"updates/{Path(rel_posix).name}" in remote_hashes
				)
			):
				# Remote server merges PatchNNN.temir into last_version/*.temir in-place;
				# do not re-upload 2 GB base archives during incremental patch updates.
				continue
			if remote_hashes:
				local_hash = sha256_file(disk_path)
				if remote_hashes.get(rel_posix) == local_hash:
					continue
			changed.append((rel_posix, disk_path))

		if not changed:
			print("[deploy] Uzak sunucudaki tum dosyalar yerel linux-server ile birebir ayni (degisiklik yok).")
		else:
			for rel_posix, disk_path in changed[:25]:
				kb = disk_path.stat().st_size / 1024.0
				print(f"  + {rel_posix} ({kb:.1f} KB)")
			if len(changed) > 25:
				print(f"  ... ve {len(changed) - 25} dosya daha")

			if mode == "sftp":
				deploy_via_sftp(target, port, key_file, remote_dir, changed, env)
			else:
				deploy_via_ssh_tar(target, port, key_file, remote_dir, changed, password, env)

		if should_restart:
			print("===========================================================================")
			print("[3/3] Uzak sunucu servisleri yeniden devreye aliniyor...")
			print("===========================================================================")
			restart_remote_services(target, port, key_file, remote_dir, args.only_updates, password, env)

		print("===========================================================================")
		print(f"DEPLOY TAMAMLANDI: {target}:{remote_dir}")
		print("===========================================================================")
		return 0
	finally:
		if askpass_tmp and askpass_tmp.exists():
			shutil.rmtree(askpass_tmp, ignore_errors=True)


if __name__ == "__main__":
	sys.exit(main())

