"""Inspect Git index content before publishing; never print matched secrets."""

from __future__ import annotations

import json
import re
import subprocess
import sys
from pathlib import Path, PurePosixPath


MAX_FILE_BYTES = 2 * 1024 * 1024
BLOCKED_DIRS = {
    ".venv", ".uv-cache", ".ruff_cache", ".pytest_cache", "__pycache__",
    "node_modules", "artifacts", "tmp",
}
BLOCKED_NAMES = {
    "credentials.json", "master.key", "binding.key", "id_ed25519", "id_rsa",
    "host_key", "model.local.json",
    "售前AI任务自动监测与诊断系统-开发设计文档-v1.md",
}
BLOCKED_SUFFIXES = {
    ".pem", ".key", ".p12", ".pfx", ".zip", ".tar", ".7z", ".rar",
    ".dump", ".db", ".sqlite", ".sqlite3", ".exe", ".dll", ".log",
}
PATTERNS = {
    "private_key": re.compile(rb"-----BEGIN (?:OPENSSH |RSA |EC |DSA |ENCRYPTED )?PRIVATE KEY-----"),
    "github_token": re.compile(rb"\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,})\b"),
    "aws_access_key": re.compile(rb"\bAKIA[0-9A-Z]{16}\b"),
    "model_key": re.compile(rb"\bsk-(?:proj-)?[A-Za-z0-9_-]{32,}\b"),
}


def git(root: Path, *args: str, payload: bytes | None = None) -> bytes:
    # Capture stderr: external Git helpers must not leak credential-bearing output.
    result = subprocess.run(
        ["git", "-c", "core.quotepath=false", *args], cwd=root,
        input=payload, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        check=False,
    )
    if result.returncode:
        raise RuntimeError("Git inspection failed; verify the repository and index.")
    return result.stdout


def private_local_values(root: Path) -> set[bytes]:
    """Read existing instance secrets locally, for exact-match comparison only."""
    values: set[bytes] = set()

    def remember(value: object) -> None:
        if isinstance(value, str) and 12 <= len(value) <= 65536:
            raw = value.strip().encode("utf-8")
            if len(raw) >= 12 and not value.startswith("${"):
                values.add(raw)

    def walk(obj: object) -> None:
        if isinstance(obj, dict):
            for key, value in obj.items():
                if str(key).lower() in {"api_key", "admin_token", "password", "postgres_password", "secret"}:
                    remember(value)
                walk(value)
        elif isinstance(obj, list):
            for value in obj:
                walk(value)

    for name in (".env", "deploy/.env"):
        path = root / name
        if path.is_file() and not path.is_symlink():
            for line in path.read_text(encoding="utf-8-sig").splitlines():
                key, separator, value = line.partition("=")
                if separator and re.search(r"(?:TOKEN|PASSWORD|API_KEY|SECRET|DSN|DATABASE_URL)$", key):
                    remember(value)

    local_files = [root / "deploy/model.local.json"]
    config_dir = root / "deploy/phase2/config"
    if config_dir.is_dir() and not config_dir.is_symlink():
        local_files.extend(p for p in config_dir.iterdir() if p.is_file() and not p.is_symlink())
    for path in local_files:
        if not path.is_file() or path.is_symlink() or path.stat().st_size > MAX_FILE_BYTES:
            continue
        if path.suffix == ".json":
            try:
                walk(json.loads(path.read_text(encoding="utf-8-sig")))
            except (ValueError, UnicodeError):
                raise RuntimeError("A local private JSON file cannot be inspected; no values were printed.") from None
        elif path.suffix in {".key", ".txt"}:
            remember(path.read_text(encoding="utf-8-sig"))
    return values


def blocked_path(name: str) -> bool:
    path = PurePosixPath(name)
    lower = path.name.lower()
    return (
        any(part.lower() in BLOCKED_DIRS for part in path.parts)
        or path.name in BLOCKED_NAMES
        or (lower.startswith(".env") and lower != ".env.example")
        or path.suffix.lower() in BLOCKED_SUFFIXES
        or lower.endswith(".tar.gz")
        or name.startswith("deploy/phase2/config/")
        or name.startswith("webui/dist/assets/")
        or name.startswith("web/dist/")
    )


def main() -> int:
    root = Path(__file__).resolve().parent.parent
    records = git(root, "ls-files", "--stage", "-z").split(b"\0")
    entries: list[tuple[str, str, str]] = []
    for record in records:
        if not record:
            continue
        metadata, raw_path = record.split(b"\t", 1)
        mode, oid, stage = metadata.decode("ascii").split()
        if stage != "0":
            raise RuntimeError("Resolve merge conflicts before publishing.")
        name = raw_path.decode("utf-8")
        if any(ord(char) < 32 for char in name):
            raise RuntimeError("Unsupported control character in a tracked path.")
        entries.append((mode, oid, name))
    if not entries:
        raise RuntimeError("The index is empty; stage the intended source files first.")

    known_secrets = private_local_values(root)
    output = git(root, "cat-file", "--batch", payload="".join(oid + "\n" for _, oid, _ in entries).encode("ascii"))
    offset, total_bytes = 0, 0
    findings: list[dict[str, object]] = []
    for mode, oid, name in entries:
        end = output.index(b"\n", offset)
        header = output[offset:end].decode("ascii").split()
        if len(header) != 3 or header[0] != oid or header[1] != "blob":
            raise RuntimeError("Unexpected Git object; publishing inspection stopped.")
        size = int(header[2])
        content = output[end + 1:end + 1 + size]
        offset = end + size + 2
        total_bytes += size

        def finding(rule: str, position: int | None = None) -> None:
            item: dict[str, object] = {"path": name, "rule": rule}
            if position is not None:
                item["line"] = content[:position].count(b"\n") + 1
            findings.append(item)

        if mode not in {"100644", "100755"}:
            finding("unsupported_file_mode")
        if blocked_path(name):
            finding("private_or_generated_path")
        if size > MAX_FILE_BYTES:
            finding("large_file")
        if b"\0" in content:
            finding("binary_file")
        for rule, pattern in PATTERNS.items():
            match = pattern.search(content)
            if match:
                finding(rule, match.start())
        for secret in known_secrets:
            position = content.find(secret)
            if position >= 0:
                finding("known_local_secret", position)
                break

    result = {
        "passed": not findings, "files": len(entries), "total_bytes": total_bytes,
        "known_secret_comparison": True, "findings": findings,
    }
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0 if not findings else 1


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (RuntimeError, OSError, ValueError, UnicodeError):
        print("Publishing inspection failed; check repository and local file access. Secret values are not displayed.", file=sys.stderr)
        sys.exit(2)
