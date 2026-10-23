from __future__ import annotations

from pathlib import Path

from datetime import timedelta

from sqlalchemy import update

from musik.config import get_settings
from musik.db.models import Job
from musik.db.schema import connect, init_db
from musik.jobs.queue import claim_next, enqueue_job, finish_job, get_job, recover_stale_running


def test_job_queue_uses_mapped_sqlalchemy_model_and_claims_once(
    monkeypatch, tmp_path: Path, test_database
) -> None:
    db_path = tmp_path / "jobs.sqlite"
    test_database(db_path)
    get_settings.cache_clear()
    try:
        init_db()
        queued = enqueue_job("scan", {"library": "/музыка"})
        claimed = claim_next()

        assert claimed is not None
        assert claimed["id"] == queued["id"]
        assert claimed["status"] == "running"
        assert claimed["payload"] == {"library": "/музыка"}
        assert claim_next() is None

        finish_job(claimed["id"], {"tracks": 3})
        finished = get_job(claimed["id"])
        assert finished is not None
        assert finished["status"] == "done"
        assert finished["result"] == {"tracks": 3}
    finally:
        get_settings.cache_clear()


def test_stale_running_job_returns_to_pending(
    monkeypatch, tmp_path: Path, test_database
) -> None:
    db_path = tmp_path / "jobs-stale.sqlite"
    test_database(db_path)
    get_settings.cache_clear()
    try:
        init_db()
        queued = enqueue_job("scan", {"library": "/музыка"})
        claimed = claim_next()
        assert claimed is not None

        assert recover_stale_running(max_age=timedelta(hours=6)) == []
        assert get_job(claimed["id"])["status"] == "running"

        with connect() as conn:
            conn.session.execute(
                update(Job)
                .where(Job.id == claimed["id"])
                .values(updated_at="2000-01-01T00:00:00+00:00")
            )

        recovered = recover_stale_running(max_age=timedelta(hours=6))
        assert [job["id"] for job in recovered] == [queued["id"]]
        restored = get_job(queued["id"])
        assert restored is not None
        assert restored["status"] == "pending"
        assert restored["error"] is None
        assert restored["result"] is None

        again = claim_next()
        assert again is not None
        assert again["id"] == queued["id"]
        assert again["status"] == "running"
    finally:
        get_settings.cache_clear()
