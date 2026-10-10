"""Real model, latency and bounded experimental soak acceptance; synthetic data only."""
import argparse
import json
import subprocess
import time
from datetime import UTC, datetime
import lab_client as lab


def events(task):
    return lab.request("GET", lab.MON + "/internal/events", params={"task_id": task, "limit": 500})


def resource_sample():
    result = subprocess.run(["docker", "stats", "--no-stream", "--format", "{{json .}}", "bid-mon-lab-monitor-1"],
                            capture_output=True, text=True, check=True)
    stats = json.loads(result.stdout)
    state = subprocess.run(["docker", "inspect", "--format", '{{json .RestartCount}}', "bid-mon-lab-monitor-1"],
                           capture_output=True, text=True, check=True)
    return {"at": datetime.now(UTC).isoformat(), "cpu": stats["CPUPerc"], "memory": stats["MemUsage"],
            "pids": stats["PIDs"], "restarts": json.loads(state.stdout),
            "metrics": lab.request("GET", lab.MON + "/internal/metrics")}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=["model", "soak"], default="model")
    parser.add_argument("--rounds", type=int, default=12)
    parser.add_argument("--interval", type=int, default=30)
    parser.add_argument("--scenario", choices=["model_quota", "log_injection", "extraction_failed"])
    args = parser.parse_args()
    record = {"at": datetime.now(UTC).isoformat(), "mode": args.mode, "samples": [], "resources": [], "passed": False}
    suffix = "-" + args.scenario if args.scenario else ""
    target = lab.ROOT / f"artifacts/go-{args.mode}{suffix}-acceptance.json"
    try:
        def idle():
            metrics = lab.request("GET", lab.MON + "/internal/metrics")
            jobs = metrics["diagnosis"]
            return not jobs.get("queued", 0) and not jobs.get("running", 0)
        lab.wait_for(idle, "initial_diagnosis_queue_idle", 180)
        if args.mode == "model":
            for scenario, category in [("model_quota", "model_quota_exhausted"), ("log_injection", "insufficient_evidence"), ("extraction_failed", "extraction_failed")]:
                if args.scenario and args.scenario != scenario:
                    continue
                begin = time.monotonic()
                task = lab.create(scenario)
                event = lab.wait_for(lambda task=task: next(iter(events(task)), None), scenario+"_detected", 35)
                latency = time.monotonic() - begin
                payload = lab.wait_for(lambda event=event: lab.report_for(event["event_id"]), scenario+"_model_report", 130)
                report = payload["report"]
                sample = {"scenario": scenario, "task_id": task, "event_id": event["event_id"], "detection_seconds": round(latency, 3),
                          "mode": report["diagnosis_mode"], "model_status": report.get("model_status"),
                          "model_error": report.get("model_error"), "category": report["category"],
                          "report_seconds": round(time.monotonic()-begin, 3)}
                record["samples"].append(sample)
                assert latency <= 30, "Detection exceeded 30 seconds"
                assert report["diagnosis_mode"] == "model_assisted", "Actual model call failed: " + str(report.get("model_error"))
                assert report["category"] == category and report["business_recovery"] == "not_confirmed"
                assert report["model_analysis"]["text"] and report["model_recommended_actions"]
                available = {e["evidence_id"] for e in payload["evidence"] if not e.get("unavailable_reason")}
                assert set(report["model_analysis"]["evidence_ids"]) <= available
                encoded = json.dumps(report)
                assert "synthetic-secret" not in encoded and "13800138000" not in encoded
                lab.wait_for(lambda event=event: lab.received(event["event_id"], "diagnosis"), scenario+"_model_notification")
        else:
            # One abnormal + two normal/canceled tasks per interval, no deliberate outage.
            begin = time.monotonic()
            normals, abnormal = [], []
            record["resources"].append(resource_sample())
            for n in range(args.rounds):
                cycle = time.monotonic()
                scenario = ["model_quota", "model_rate_limit", "model_timeout", "extraction_failed"][n % 4]
                task = lab.create(scenario)
                abnormal.append(task)
                normals.extend([lab.create("normal"), lab.create("canceled")])
                event = lab.wait_for(lambda task=task: next(iter(events(task)), None), f"round_{n+1}_discovered", 35)
                elapsed = time.monotonic()-cycle
                record["samples"].append({"scenario": scenario, "task_id": task, "event_id": event["event_id"], "detection_seconds": round(elapsed, 3)})
                assert elapsed <= 30, "Discovery latency exceeded target"
                if n % 3 == 2:
                    record["resources"].append(resource_sample())
                remaining = args.interval - (time.monotonic()-cycle)
                if remaining > 0:
                    time.sleep(remaining)
            for task in abnormal:
                rows = events(task)
                assert len(rows) == 1, "Duplicate event"
                payload = lab.wait_for(lambda rows=rows: lab.report_for(rows[0]["event_id"]), "soak_report", 130)
                report = payload["report"]
                assert report["diagnosis_status"] == "completed", "Job not completed"
                record["samples"][abnormal.index(task)]["model_status"] = report.get("model_status")
                record["samples"][abnormal.index(task)]["model_error"] = report.get("model_error")
                lab.wait_for(lambda rows=rows: lab.received(rows[0]["event_id"], "initial"), "soak_initial_notice")
                lab.wait_for(lambda rows=rows: lab.received(rows[0]["event_id"], "diagnosis"), "soak_diagnosis_notice")
            for task in normals:
                assert not events(task), "False positive on normal/canceled task"
            record["resources"].append(resource_sample())
            assert len({s["restarts"] for s in record["resources"]}) == 1, "Unexpected restart"
            assert all(s["metrics"]["source_failures"] == 0 for s in record["resources"]), "Source failures"
            final_jobs = record["resources"][-1]["metrics"]["diagnosis"]
            assert not final_jobs.get("queued", 0) and not final_jobs.get("running", 0), "Diagnosis queue did not drain"
            record["model_completed"] = sum(s.get("model_status") == "completed" for s in record["samples"])
            record["model_fallback"] = sum(s.get("model_status") == "fallback" for s in record["samples"])
            record["max_detection_seconds"] = max(s["detection_seconds"] for s in record["samples"])
            record["duration_seconds"] = round(time.monotonic()-begin, 3)
            record["tasks_created"] = len(normals)+len(abnormal)
        record["passed"] = True
        print("PASS " + args.mode + " acceptance", flush=True)
    finally:
        target.write_text(json.dumps(record, ensure_ascii=False, indent=2), encoding="utf-8")


if __name__ == "__main__":
    main()
