"""Run Go tests in the real lab and save JSON results without exposing credentials."""

import json
import subprocess
from datetime import UTC, datetime
from pathlib import Path

root = Path(__file__).resolve().parents[1]
compose = ["docker", "compose", "--env-file", "deploy/.env", "-f", "deploy/compose.go.yml"]
command = [*compose, "run", "--rm", "--no-deps", "go-tests", "go", "test", "-race", "-count=1", "-json", "./internal/..."]
try:
    startup = subprocess.run([*compose, "up", "-d", "--wait", "test-mysql"], cwd=root, capture_output=True)
    if startup.returncode:
        raise RuntimeError("Isolated test MySQL did not become healthy")
    result = subprocess.run(command, cwd=root, capture_output=True, text=True, encoding="utf-8", errors="replace")
finally:
    subprocess.run([*compose, "stop", "test-mysql"], cwd=root, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
events = []
for line in result.stdout.splitlines():
    try:
        row = json.loads(line)
    except ValueError:
        continue
    events.append(row)
    if row.get("Action") == "fail" or "FAIL" in row.get("Output", ""):
        print(row.get("Output", row.get("Test", "package failed")).strip())
passed = [row["Test"] for row in events if row.get("Action") == "pass" and row.get("Test")]
skipped = [row["Test"] for row in events if row.get("Action") == "skip" and row.get("Test")]
target = root / "artifacts/go-tests.json"
target.parent.mkdir(exist_ok=True)
target.write_text(json.dumps({"at": datetime.now(UTC).isoformat(), "exit_code": result.returncode,
                             "top_level_passed": len([name for name in passed if "/" not in name]),
                             "passed_including_subtests": len(passed), "skipped": skipped, "events": events},
                            ensure_ascii=False, indent=2), encoding="utf-8")
print(f"Go tests: {len(passed)} passed including subtests, {len(skipped)} skipped; exit {result.returncode}")
if result.returncode and not events:
    print("Docker/test startup failed; inspect compose service state.")
raise SystemExit(result.returncode)
