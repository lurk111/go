"""Display only synthetic diagnostic text for local review; never read model credentials."""
import json
import lab_client as lab

rows = lab.request("GET", lab.MON + "/internal/events?limit=4")
for event in rows:
    payload = lab.report_for(event["event_id"])
    if not payload:
        continue
    report = payload["report"]
    print(json.dumps({"category": report.get("category"), "mode": report.get("diagnosis_mode"),
                      "analysis": report.get("model_analysis"),
                      "actions": report.get("model_recommended_actions")}, ensure_ascii=False))
    if report.get("category") == "extraction_failed":
        print(json.dumps({"evidence": payload["evidence"]}, ensure_ascii=False))
