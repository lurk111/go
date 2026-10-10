"""Acceptance against the independent Go stack; faults are always restored."""

import json
import subprocess
from pathlib import Path
import lab_client as lab


def compose(*args):
    subprocess.run(["docker", "compose", "--env-file", "deploy/.env", "-f", "deploy/compose.go.yml", *args],
                   cwd=lab.ROOT, check=True, stdout=subprocess.DEVNULL)


def main():
    lab.record["implementation"] = "independent_go"
    lab.wait_for(lambda: lab.client.get(lab.MON+"/health/ready").status_code == 200, "go_ready")
    assert lab.request("GET", lab.MON+"/health/live")["implementation"] == "go"
    normals = [lab.create(s) for s in ("normal", "canceled", "delayed_result")]
    expected = {"model_quota":"model_quota_exhausted", "model_rate_limit":"model_rate_limited",
                "model_timeout":"model_timeout", "extraction_failed":"extraction_failed",
                "log_injection":"insufficient_evidence"}
    # Capacity is exercised separately by accept_go_model.py --mode soak.
    # Keep fault regression scenarios independent of the external model queue.
    tasks = {}
    events = {}
    for scenario in expected:
        task = lab.create(scenario)
        tasks[scenario] = task
        event = lab.wait_for(lambda t=task:lab.event_for(t), scenario+"_discovered")
        events[scenario] = event
        lab.record["events"][scenario] = event["event_id"]
        payload = lab.wait_for(lambda e=event:lab.report_for(e["event_id"]), scenario+"_report")
        report = payload["report"]
        assert report["diagnosis_mode"] in ("rule_based", "model_assisted") and report["category"] == expected[scenario], report["category"]
        assert report["business_recovery"] == "not_confirmed"
        lab.wait_for(lambda e=event:lab.received(e["event_id"], "diagnosis"), scenario+"_notified")
    empty = lab.create("empty",40)
    lab.wait_for(lambda:any(e["task_id"]==empty and e["rule_code"]=="RESULT_EMPTY_SUSPECT"
                           for e in lab.request("GET",lab.MON+"/internal/events?limit=500")), "empty_result_candidate")
    event = events["model_quota"]
    lab.request("POST",lab.MON+f"/internal/events/{event['event_id']}/close",json={"reason":"这是实验故意制造的失败，不需要继续处理"})
    lab.wait_for(lambda:lab.received(event["event_id"],"closed"),"manual_close_notice")
    compose("restart","monitor")
    lab.wait_for(lambda:lab.client.get(lab.MON+"/health/ready").status_code==200,"restart_ready")
    closed = lab.event_for(tasks["model_quota"])
    assert closed["event_id"] == event["event_id"] and closed["status"] == "closed_unresolved"
    lab.record["checks"].append("closed_event_survives_restart")
    for task in normals:
        assert lab.event_for(task) is None, "normal/canceled/delayed task falsely alerted"
    lab.record["checks"].append("normal_cancel_delay_no_false_alerts")
    task = tasks["model_timeout"]
    lab.request("POST",lab.LAB+f"/tasks/{task}/transition",json={"status":"completed","with_result":True})
    lab.wait_for(lambda:lab.event_for(task)["status"]=="resolved","business_recovery_confirmed")
    try:
        compose("stop","ssh-target")
        task = lab.create("failed")
        event = lab.wait_for(lambda:lab.event_for(task),"ssh_outage_event")
        lab.wait_for(lambda:lab.received(event["event_id"],"initial"),"initial_notice_without_ssh")
        report = lab.wait_for(lambda:lab.report_for(event["event_id"]),"ssh_outage_report")
        assert report["report"]["certainty"]=="insufficient_evidence"
    finally:
        compose("start","ssh-target")
    try:
        compose("stop","worker")
        task = lab.create("pending",190)
        lab.request("POST",lab.LAB+f"/tasks/{task}/transition",json={"status":"pending"})
        event = lab.wait_for(lambda:lab.event_for(task),"worker_queue_event")
        report = lab.wait_for(lambda:lab.report_for(event["event_id"]),"worker_heartbeat_report")
        assert report["report"]["category"]=="worker_unavailable_suspect",report["report"]["category"]
    finally:
        compose("start","worker")
    old = Path(lab.ROOT/"artifacts/akmatori-smoke.json")
    if old.exists():
        prior=json.loads(old.read_text(encoding="utf-8"))["events"]["0"]
        rows=lab.request("GET",lab.MON+"/internal/events?limit=500&status=closed_unresolved")
        prior_event=next(e for e in rows if e["event_id"]==prior)
        assert prior_event["close_reason"]=="这是实验故意制造的失败，不需要继续处理"
        prior_report=lab.request("GET",lab.MON+f"/internal/events/{prior}/report")
        assert prior_report["report"]
        lab.record["checks"].append("legacy_closed_event_and_report_preserved")
    target=lab.ROOT/"artifacts/go-smoke.json"
    target.write_text(json.dumps(lab.record,ensure_ascii=False,indent=2),encoding="utf-8")
    print(f"PASS {len(lab.record['checks'])} Go end-to-end checks; record saved",flush=True)


if __name__=="__main__":
    main()
