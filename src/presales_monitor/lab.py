"""Synthetic business service and independently runnable worker. No company code."""

import json
import os
import time
from datetime import timedelta
from typing import Literal
from uuid import uuid4

from fastapi import Depends, FastAPI, HTTPException
from pydantic import BaseModel, Field
from sqlalchemy import create_engine, text

from .lab_contract import utcnow, INSTRUCTION_FIELDS, LIST_PATHS
from .security import authenticate
from .lab_evidence import emit, heartbeat, scenario_evidence, order_evidence


def result_fixture(empty=False):
    result = {"bidder_instructions": {k: {"value": None, "source": None} for k in INSTRUCTION_FIELDS}}
    for path in LIST_PATHS:
        parts = path.split(".")
        if len(parts) == 2:
            result[parts[0]] = {parts[1]: []}
        else:
            result[path] = []
    if not empty:
        result["technical_requirements"]["items"] = [
            {"title": "合成要求", "content": "支持实验任务", "source": "合成文档"}
        ]
    return result


def engine():
    return create_engine(
        os.environ["LAB_DATABASE_URL"],
        pool_pre_ping=True,
        connect_args={"init_command": "SET time_zone = '+00:00'", "connect_timeout": 5},
    )


app = FastAPI(title="合成任务实验服务")


class Scenario(BaseModel):
    scenario: Literal[
        "normal",
        "failed",
        "pending",
        "running",
        "canceled",
        "empty",
        "delayed_result",
        "missing_result",
        "invalid_result",
        "duplicate_result",
        "unknown",
        "model_quota",
        "model_rate_limit",
        "model_timeout",
        "extraction_failed",
        "log_injection",
    ]
    count: int = Field(default=1, ge=1, le=1000)
    age_seconds: int = Field(default=0, ge=0, le=172800)
    delay_seconds: int = Field(default=3, ge=0, le=3600)


def create_scenarios(db, body):
    ids = []
    with db.begin() as c:
        for _ in range(body.count):
            task_id = "lab-" + uuid4().hex[:20]
            now = utcnow().replace(tzinfo=None)
            created = now - timedelta(seconds=body.age_seconds)
            status = {
                "failed": "failed",
                "pending": "pending",
                "running": "running",
                "canceled": "canceled",
                "unknown": "future_state",
                "normal": "pending",
                "model_quota": "failed",
                "model_rate_limit": "failed",
                "model_timeout": "failed",
                "extraction_failed": "failed",
                "log_injection": "failed",
            }.get(body.scenario, "completed")
            c.execute(
                text(
                    "INSERT INTO async_tasks(task_id,biz_type,user_id,status,stage,created_at,updated_at,"
                    "started_at,finished_at,error_message,worker_id) VALUES "
                    "(:id,'bidding','synthetic-user',:status,:stage,:created,:created,:started,:finished,:error,'lab-worker')"
                ),
                {
                    "id": task_id,
                    "status": status,
                    "stage": "analyze"
                    if status == "running"
                    else "finalize"
                    if status == "completed"
                    else status,
                    "created": created,
                    "started": created if status in {"running", "completed", "failed"} else None,
                    "finished": created if status in {"completed", "failed", "canceled"} else None,
                    "error": "Synthetic extraction failure" if status == "failed" else None,
                },
            )
            if status == "completed" and body.scenario not in {"delayed_result", "missing_result"}:
                raw = (
                    "{broken"
                    if body.scenario == "invalid_result"
                    else json.dumps(result_fixture(body.scenario == "empty"))
                )
                for _ in range(2 if body.scenario == "duplicate_result" else 1):
                    c.execute(
                        text(
                            "INSERT INTO bidding_results(task_id,user_id,status,file_count,result_json) "
                            "VALUES (:id,'synthetic-user','completed',1,:raw)"
                        ),
                        {"id": task_id, "raw": raw},
                    )
            if body.scenario in {"normal", "delayed_result"}:
                c.execute(
                    text("INSERT INTO lab_jobs(task_id,action,due_at) VALUES(:id,:action,:due)"),
                    {
                        "id": task_id,
                        "action": "start" if body.scenario == "normal" else "result",
                        "due": now + timedelta(seconds=body.delay_seconds),
                    },
                )
            ids.append(task_id)
            scenario_evidence(task_id, body.scenario)
    return ids


@app.post("/scenarios", dependencies=[Depends(authenticate)])
def scenarios(body: Scenario):
    db = engine()
    try:
        return {"task_ids": create_scenarios(db, body)}
    finally:
        db.dispose()


class Transition(BaseModel):
    status: Literal["pending", "running", "completed", "failed", "canceled"]
    with_result: bool = True


@app.post("/tasks/{task_id}/transition", dependencies=[Depends(authenticate)])
def transition(task_id: str, body: Transition):
    db = engine()
    try:
        with db.begin() as c:
            result = c.execute(
                text(
                    "UPDATE async_tasks SET status=:status,stage=:status,updated_at=UTC_TIMESTAMP(6),"
                    "finished_at=CASE WHEN :status IN ('completed','failed','canceled') THEN UTC_TIMESTAMP(6) ELSE NULL END "
                    "WHERE task_id=:id"
                ),
                {"status": body.status, "id": task_id},
            )
            if not result.rowcount:
                raise HTTPException(404, "Task not found")
            if body.status == "completed" and body.with_result:
                c.execute(text("DELETE FROM bidding_results WHERE task_id=:id"), {"id": task_id})
                c.execute(
                    text(
                        "INSERT INTO bidding_results(task_id,user_id,status,file_count,result_json) "
                        "VALUES (:id,'synthetic-user','completed',1,:raw)"
                    ),
                    {"id": task_id, "raw": json.dumps(result_fixture())},
                )
        return {"task_id": task_id, "status": body.status}
    finally:
        db.dispose()


@app.get("/health/live")
def live():
    return {"status": "alive", "mode": "synthetic"}


class OrderScenario(BaseModel):
    callback_state: Literal["WAITING_ACK", "DELIVERED", "DEAD_LETTER", "CANCELED"]
    count: int = Field(default=1, ge=1, le=1000)
    age_seconds: int = Field(default=0, ge=0, le=172800)
    retry_count: int = Field(default=3, ge=0, le=100)


@app.post("/order-scenarios", dependencies=[Depends(authenticate)])
def order_scenarios(body: OrderScenario):
    db = engine()
    ids = []
    try:
        with db.begin() as c:
            for _ in range(body.count):
                order_id = "order-" + uuid4().hex[:20]
                at = utcnow().replace(tzinfo=None) - timedelta(seconds=body.age_seconds)
                c.execute(text("INSERT INTO callback_orders(order_id,callback_state,retry_count,created_at,updated_at) "
                               "VALUES(:id,:status,:retries,:at,:at)"),
                          {"id": order_id, "status": body.callback_state, "retries": body.retry_count, "at": at})
                ids.append(order_id)
                order_evidence(order_id, body.callback_state)
        return {"order_ids": ids, "synthetic": True}
    finally:
        db.dispose()


class OrderTransition(BaseModel):
    callback_state: Literal["WAITING_ACK", "DELIVERED", "DEAD_LETTER", "CANCELED"]


@app.post("/orders/{order_id}/transition", dependencies=[Depends(authenticate)])
def order_transition(order_id: str, body: OrderTransition):
    db = engine()
    try:
        with db.begin() as c:
            result = c.execute(text("UPDATE callback_orders SET callback_state=:status,updated_at=UTC_TIMESTAMP(6) "
                                    "WHERE order_id=:id"), {"id": order_id, "status": body.callback_state})
            if not result.rowcount:
                raise HTTPException(404, "Order not found")
        order_evidence(order_id, body.callback_state)
        return {"order_id": order_id, "callback_state": body.callback_state, "synthetic": True}
    finally:
        db.dispose()


def worker_tick(db):
    heartbeat()
    with db.begin() as c:
        job = (
            c.execute(
                text(
                    "SELECT id,task_id,action FROM lab_jobs WHERE due_at<=UTC_TIMESTAMP(6) "
                    "ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED"
                )
            )
            .mappings()
            .first()
        )
        if not job:
            return
        if job["action"] == "start":
            c.execute(
                text(
                    "UPDATE async_tasks SET status='running',stage='analyze',started_at=UTC_TIMESTAMP(6),"
                    "updated_at=UTC_TIMESTAMP(6) WHERE task_id=:id AND status='pending'"
                ),
                {"id": job["task_id"]},
            )
            c.execute(
                text(
                    "UPDATE lab_jobs SET action='finish',due_at=UTC_TIMESTAMP(6)+INTERVAL 1 SECOND WHERE id=:id"
                ),
                {"id": job["id"]},
            )
        else:
            c.execute(
                text(
                    "INSERT INTO bidding_results(task_id,user_id,status,file_count,result_json) "
                    "VALUES(:id,'synthetic-user','completed',1,:raw)"
                ),
                {"id": job["task_id"], "raw": json.dumps(result_fixture())},
            )
            if job["action"] == "finish":
                c.execute(
                    text(
                        "UPDATE async_tasks SET status='completed',stage='finalize',updated_at=UTC_TIMESTAMP(6),"
                        "finished_at=UTC_TIMESTAMP(6) WHERE task_id=:id AND status='running'"
                    ),
                    {"id": job["task_id"]},
                )
            c.execute(text("DELETE FROM lab_jobs WHERE id=:id"), {"id": job["id"]})
        print(json.dumps({"task_id": job["task_id"], "action": job["action"], "synthetic": True}), flush=True)
        emit(job["task_id"], "message", message="Worker phase advanced", action=job["action"])


if __name__ == "__main__":
    db = engine()
    while True:
        worker_tick(db)
        time.sleep(0.25)
