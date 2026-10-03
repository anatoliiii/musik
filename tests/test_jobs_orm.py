from __future__ import annotations

from pathlib import Path

from musik.config import get_settings
from musik.db.schema import init_db
from musik.jobs.queue import claim_next, enqueue_job, finish_job, get_job


def test_job_queue_uses_mapped_sqlalchemy_model_and_claims_once(
    monkeypatch, tmp_path: Path
) -> None:
    db_path = tmp_path / "jobs.sqlite"
    monkeypatch.delenv("MUSIK_DATABASE_URL", raising=False)
    monkeypatch.setenv("MUSIK_DB_PATH", str(db_path))
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
