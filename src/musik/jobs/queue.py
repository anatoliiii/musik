"""SQLAlchemy-backed job queue for the selected database."""

from __future__ import annotations

import json
from typing import Any

from sqlalchemy import select, update

from musik.db.schema import connect, row_to_dict, utcnow
from musik.db.models import Job


def _job_row(row: Any) -> dict[str, Any] | None:
    d = row_to_dict(row)
    if d is None:
        return None
    if d.get("payload_json"):
        try:
            d["payload"] = json.loads(d["payload_json"])
        except json.JSONDecodeError:
            d["payload"] = None
    else:
        d["payload"] = None
    if d.get("result_json"):
        try:
            d["result"] = json.loads(d["result_json"])
        except json.JSONDecodeError:
            d["result"] = None
    else:
        d["result"] = None
    return d


def enqueue_job(kind: str, payload: dict[str, Any] | None = None) -> dict[str, Any]:
    now = utcnow()
    payload_json = json.dumps(payload or {}, ensure_ascii=False)
    with connect() as conn:
        job = Job(kind=kind, status="pending", payload_json=payload_json,
                  created_at=now, updated_at=now)
        conn.session.add(job)
        conn.session.flush()
        return _job_row(_job_mapping(job)) or {}


def enqueue_job_once(
    kind: str,
    payload: dict[str, Any],
) -> dict[str, Any] | None:
    """Atomically enqueue an exact kind/payload pair unless it already exists."""
    now = utcnow()
    payload_json = json.dumps(payload, ensure_ascii=False, sort_keys=True)
    with connect() as conn:
        existing = conn.session.scalar(
            select(Job).where(Job.kind == kind, Job.payload_json == payload_json)
            .order_by(Job.id.desc()).limit(1)
        )
        if existing is not None:
            return None
        job = Job(kind=kind, status="pending", payload_json=payload_json,
                  created_at=now, updated_at=now)
        conn.session.add(job)
        conn.session.flush()
        return _job_row(_job_mapping(job))


def claim_next() -> dict[str, Any] | None:
    now = utcnow()
    with connect() as conn:
        next_id = select(Job.id).where(Job.status == "pending").order_by(Job.id).limit(1).scalar_subquery()
        statement = (
            update(Job).where(Job.id == next_id, Job.status == "pending")
            .values(status="running", updated_at=now).returning(Job)
        )
        job = conn.session.execute(statement).scalar_one_or_none()
        return _job_row(_job_mapping(job)) if job is not None else None


def finish_job(job_id: int, result: dict[str, Any] | None = None) -> None:
    now = utcnow()
    result_json = json.dumps(result or {}, ensure_ascii=False)
    with connect() as conn:
        conn.session.execute(
            update(Job).where(Job.id == job_id).values(
                status="done", result_json=result_json, error=None, updated_at=now
            )
        )


def fail_job(job_id: int, error: str) -> None:
    now = utcnow()
    with connect() as conn:
        conn.session.execute(
            update(Job).where(Job.id == job_id).values(
                status="failed", error=error[:4000], updated_at=now
            )
        )


def update_job_progress(job_id: int, progress: dict[str, Any]) -> None:
    """Write live progress into result_json while status=running (pollable via API)."""
    now = utcnow()
    body = {"progress": progress}
    result_json = json.dumps(body, ensure_ascii=False)
    with connect() as conn:
        conn.session.execute(
            update(Job).where(Job.id == job_id, Job.status == "running").values(
                result_json=result_json, updated_at=now
            )
        )


def get_job(job_id: int) -> dict[str, Any] | None:
    with connect() as conn:
        job = conn.session.get(Job, job_id)
        return _job_row(_job_mapping(job)) if job is not None else None


def list_recent(limit: int = 30) -> list[dict[str, Any]]:
    with connect() as conn:
        jobs = conn.session.scalars(
            select(Job).order_by(Job.id.desc()).limit(max(1, int(limit)))
        ).all()
        return [result for job in jobs if (result := _job_row(_job_mapping(job))) is not None]


def _job_mapping(job: Job | None) -> dict[str, Any] | None:
    if job is None:
        return None
    return {
        "id": job.id, "kind": job.kind, "status": job.status,
        "payload_json": job.payload_json, "result_json": job.result_json,
        "error": job.error, "created_at": job.created_at, "updated_at": job.updated_at,
    }
