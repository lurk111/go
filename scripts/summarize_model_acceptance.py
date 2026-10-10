"""Read persisted reports from a finished soak; no model requests or task writes."""
import json
from datetime import UTC, datetime
import lab_client as lab

record = json.loads((lab.ROOT / "artifacts/go-soak-acceptance.json").read_text(encoding="utf-8"))
details = []
for sample in record["samples"]:
    payload = lab.report_for(sample["event_id"])
    report = payload["report"]
    valid = {r["evidence_id"] for r in payload["evidence"] if not r.get("unavailable_reason")}
    if report["diagnosis_mode"] == "model_assisted":
        assert report["model_analysis"]["text"] and report["model_recommended_actions"]
        assert set(report["model_analysis"]["evidence_ids"]) <= valid
    assert report["business_recovery"] == "not_confirmed"
    details.append({"event_id": sample["event_id"], "category": report["category"],
                    "model_status": report.get("model_status"), "model_error": report.get("model_error")})
result = {"at": datetime.now(UTC).isoformat(), "source": "go-soak-acceptance.json",
          "passed": True, "reports": details}
(lab.ROOT / "artifacts/go-soak-model-details.json").write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
print(json.dumps(details, ensure_ascii=False))
