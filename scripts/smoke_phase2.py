"""Synthetic two-business acceptance; run inside the demo simulator container."""
import json
import os
import time
from urllib.error import HTTPError
from urllib.parse import urlencode
from urllib.request import Request, urlopen

TOKEN = os.environ["MON_ADMIN_TOKEN"]
BASE = os.environ.get("PHASE2_MONITOR_URL", "http://monitor:8000")
checks = []


def request(path, data=None, method=None, base=BASE, expected=200):
    raw = None if data is None else json.dumps(data).encode()
    req = Request(base + path, raw, {"Authorization": "Bearer " + TOKEN,
                                   "Content-Type": "application/json"}, method=method)
    try:
        with urlopen(req, timeout=12) as response:
            assert response.status == expected
            return json.load(response)
    except HTTPError as exc:
        if exc.code != expected:
            raise RuntimeError("HTTP " + str(exc.code) + " for " + path) from None
        return json.load(exc)


def until(fn, seconds=50):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        result = fn()
        if result:
            return result
        time.sleep(0.5)
    raise RuntimeError("acceptance condition timed out")


def events(object_id):
    return request("/internal/events?" + urlencode({"task_id": object_id, "status": ""}))


def mark(name):
    checks.append(name)


ready = request("/health/ready")
assert ready["ready"]
config = request("/internal/config")
doc, version = config["document"], config["version"]
assert len(doc["checks"]) >= 2 and doc["monitor_presales"]
mark("two_businesses_configured")
assert request("/internal/sources/orders/test", doc, "POST")["connected"]
mark("source_connection_test")
invalid = {**doc, "unrecognized_setting": True}
request("/internal/config", invalid, "PUT", expected=400)
assert request("/internal/config")["version"] == version
mark("invalid_configuration_not_activated")

ids = request("/order-scenarios", {"callback_state": "DEAD_LETTER", "count": 3}, base="http://simulator:8000")["order_ids"]
preview = request("/internal/checks/order_callback/preview", doc, "POST")
assert preview["dry_run"] and preview["decisions"]
assert any(row["entity_key"] == ids[0] for row in preview["observations"])
mark("dry_run_sql_and_rule_decisions")
event = until(lambda: next((e for e in events(ids[0]) if e["check_id"] == "order_callback"), None))
report = until(lambda: (lambda j: j["report"] if j.get("report") else None)(request("/internal/events/" + event["event_id"] + "/report")))
assert report["category"] == "downstream_rejected" and report["diagnosis_mode"] == "rule_based"
job = request("/internal/events/" + event["event_id"] + "/report")
assert any(row["source"] == "ssh-helper" and not row.get("unavailable_reason") for row in job["evidence"])
mark("real_ssh_logs_and_configured_cause")
aggregate = until(lambda: next((e for e in request("/internal/events?status=") if e["check_id"] == "order_failure_rate" and e["status"] == "open"), None))
mark("aggregate_backlog_event")
delivered = until(lambda: [o for o in request("/internal/outbox?event_id=" + event["event_id"]) if o["status"] == "delivered"])
assert any(o["kind"] == "initial" for o in delivered)
mark("notification_confirmation")
assert request("/internal/channels/demo_receiver/test", doc, "POST")["confirmed"]
mark("explicit_synthetic_channel_test")
probe = request("/internal/checks/order_callback/evidence-preview", {"entity_key": ids[0]}, "POST")
assert probe["dry_run"] and not probe["evidence"].get("unavailable_reason")
mark("evidence_preview")

reason = "实验故意制造的失败，无需继续处理"
closed = request("/internal/events/" + event["event_id"] + "/close", {"reason": reason}, "POST")
assert closed["status"] == "closed_unresolved" and closed["condition_active"]
time.sleep(7)
assert len(events(ids[0])) == 1 and events(ids[0])[0]["close_reason"] == reason
mark("manual_close_keeps_reason_and_suppresses_reopening")
for object_id in ids:
    request("/orders/" + object_id + "/transition", {"callback_state": "DELIVERED"}, "POST", base="http://simulator:8000")
until(lambda: not events(ids[0])[0]["condition_active"])
assert events(ids[0])[0]["status"] == "closed_unresolved"
until(lambda: next((e for e in request("/internal/events?status=") if e["event_id"] == aggregate["event_id"] and e["status"] == "resolved"), None))
mark("explicit_entity_and_aggregate_recovery")
request("/orders/" + ids[0] + "/transition", {"callback_state": "DEAD_LETTER"}, "POST", base="http://simulator:8000")
recurrence = until(lambda: next((e for e in events(ids[0]) if e["episode"] == 2), None))
assert recurrence["status"] == "open" and recurrence["event_id"] != event["event_id"]
mark("genuine_recurrence_new_episode")
request("/orders/" + ids[0] + "/transition", {"callback_state": "DELIVERED"}, "POST", base="http://simulator:8000")
until(lambda: next((e for e in events(ids[0]) if e["episode"] == 2 and e["status"] == "resolved"), None))

task = request("/scenarios", {"scenario": "failed"}, base="http://simulator:8000")["task_ids"][0]
legacy = until(lambda: next((e for e in events(task) if e["rule_code"] == "TASK_FAILED"), None))
request("/internal/events/" + legacy["event_id"] + "/close", {"reason": reason}, "POST")
mark("legacy_presales_monitor_still_operates")
before = len(request("/internal/events?status="))
retention = request("/internal/retention/preview", {"closed_evidence_days": 30, "snapshot_days": 30, "delivered_days": 30}, "POST")
assert retention["preview_only"] and not retention["automatic_cleanup"]
assert len(request("/internal/events?status=")) == before
mark("retention_preview_only")
print(json.dumps({"passed": len(checks), "checks": checks, "model_network_calls": 0,
                  "synthetic_order_ids": ids, "manual_close_event": event["event_id"]}, ensure_ascii=False))
