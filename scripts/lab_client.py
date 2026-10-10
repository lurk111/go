"""Shared HTTP helpers for acceptance against the independent Go stack."""

import time
from datetime import UTC, datetime
from pathlib import Path

import httpx
from dotenv import dotenv_values

ROOT = Path(__file__).resolve().parents[1]
token = dotenv_values(ROOT / "deploy/.env")["MON_ADMIN_TOKEN"]
client = httpx.Client(headers={"Authorization": f"Bearer {token}"}, timeout=10, trust_env=False)
MON, LAB, SINK = "http://127.0.0.1:18080", "http://127.0.0.1:18081", "http://127.0.0.1:18082"
record = {"at": datetime.now(UTC).isoformat(), "checks": [], "events": {}}


def request(method, url, **kwargs):
    response = client.request(method, url, **kwargs)
    if response.status_code >= 400:
        raise RuntimeError(f"HTTP {response.status_code}")
    return response.json()


def wait_for(fn, label, seconds=180):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        try:
            value = fn()
            if value:
                record["checks"].append(label)
                print("PASS " + label, flush=True)
                return value
        except httpx.TransportError:
            pass
        time.sleep(1)
    raise RuntimeError("Timed out: " + label)


def create(scenario, age=0):
    return request("POST", LAB + "/scenarios", json={"scenario": scenario, "age_seconds": age})["task_ids"][0]


def event_for(task):
    return next((e for e in request("GET", MON + "/internal/events?limit=500") if e["task_id"] == task), None)


def report_for(event_id):
    payload = request("GET", MON + f"/internal/events/{event_id}/report")
    return payload if payload.get("report") else None


def received(event_id, kind):
    return any(
        m["event_id"] == event_id and m["kind"] == kind
        for m in request("GET", SINK + "/notifications?limit=1000")
    )
