"""Generate local-only secrets without printing them or overwriting existing settings."""

import secrets
from pathlib import Path

path = Path(__file__).resolve().parents[1] / "deploy" / ".env"
keys = [
    "MYSQL_ROOT_PASSWORD",
    "MYSQL_PASSWORD",
    "MONITOR_MYSQL_PASSWORD",
    "POSTGRES_PASSWORD",
    "MON_ADMIN_TOKEN",
]
if not path.exists():
    with path.open("x", encoding="utf-8") as stream:
        for key in keys:
            stream.write(f"{key}={secrets.token_hex(24)}\n")
    print("Created deploy/.env with local lab secrets. Values were not printed.")
else:
    print("Preserved existing deploy/.env.")
model_path = path.parent / "model.local.json"
if not model_path.exists():
    model_path.write_text("{}\n", encoding="utf-8")
