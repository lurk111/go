"""Local notification sink; SQLite is only its inbox, never the monitor database."""

import json
import os
import sqlite3
from contextlib import contextmanager
from pathlib import Path

from fastapi import Depends, FastAPI, Query, Response
from pydantic import BaseModel, Field

from .security import authenticate

app = FastAPI(title="本地通知接收器（非飞书）")


@contextmanager
def connect():
    path = Path(os.environ.get("SINK_DATABASE", "/data/notifications.sqlite"))
    path.parent.mkdir(parents=True, exist_ok=True)
    c = sqlite3.connect(path, timeout=5)
    c.execute(
        "CREATE TABLE IF NOT EXISTS messages (id INTEGER PRIMARY KEY, event_id TEXT, revision INTEGER, payload TEXT, UNIQUE(event_id,revision))"
    )
    c.execute(
        "CREATE TABLE IF NOT EXISTS faults (id INTEGER PRIMARY KEY CHECK(id=1), remaining INTEGER, code INTEGER)"
    )
    try:
        with c:
            yield c
    finally:
        c.close()


class Notification(BaseModel):
    event_id: str
    revision: int
    task_id: str
    rule_code: str
    episode: int
    kind: str
    status: str
    channel: str
    diagnosis_mode: str
    business_recovery: str
    observed_at: str
    summary: str
    diagnosis_status: str | None = None
    category: str | None = None
    certainty: str | None = None
    report_path: str | None = None


@app.post("/notifications", dependencies=[Depends(authenticate)])
def receive(body: Notification, response: Response):
    with connect() as c:
        fault = c.execute("SELECT remaining,code FROM faults WHERE id=1").fetchone()
        if fault and fault[0] > 0:
            c.execute("UPDATE faults SET remaining=remaining-1 WHERE id=1")
            response.status_code = fault[1]
            response.headers["Retry-After"] = "1"
            return {"code": 1}
        c.execute(
            "INSERT OR IGNORE INTO messages(event_id,revision,payload) VALUES(?,?,?)",
            (body.event_id, body.revision, body.model_dump_json()),
        )
    return {"code": 0}


@app.get("/notifications", dependencies=[Depends(authenticate)])
def messages(limit: int = Query(100, ge=1, le=1000)):
    with connect() as c:
        return [
            json.loads(row[0])
            for row in c.execute("SELECT payload FROM messages ORDER BY id DESC LIMIT ?", (limit,))
        ]


class Fault(BaseModel):
    remaining: int = Field(ge=0, le=100)
    code: int = Field(default=429, ge=400, le=599)


@app.post("/faults", dependencies=[Depends(authenticate)])
def faults(body: Fault):
    with connect() as c:
        c.execute(
            "INSERT OR REPLACE INTO faults(id,remaining,code) VALUES(1,?,?)", (body.remaining, body.code)
        )
    return {"configured": True}


@app.get("/health/live")
def live():
    return {"status": "alive", "channel": "local_sink"}
