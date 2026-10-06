"""Disposable PostgreSQL 17 migration and SQLite transfer integration test."""
from __future__ import annotations

from pathlib import Path

from sqlalchemy import text

from musik.db.migrations import LATEST_SCHEMA_VERSION, migrate_database
from musik.db.orm import engine_for
from musik.db.schema import connect, utcnow
from musik.db.transfer import transfer_sqlite_to_postgres


def test_postgres_migration_and_verified_sqlite_transfer(tmp_path: Path, empty_postgres_database: str) -> None:
    POSTGRES_URL = empty_postgres_database
    migrate_database(POSTGRES_URL, tmp_path / "unused.sqlite")
    source = tmp_path / "source.sqlite"
    migrate_database(None, source)
    now = utcnow()
    with connect(source) as db:
        profile = {"profile": db.profile_id}
        track_id = db.execute(
            """INSERT INTO tracks(path,title,artist,album,is_active,created_at,updated_at)
               VALUES('/test/transfer.flac','Transfer','Test','Album',1,:now,:now)
               RETURNING id""",
            {"now": now},
        ).scalar_one()
        db.execute(
            "INSERT INTO listening_history(track_id,ts,action,profile_id) VALUES(:track_id,:now,'finish',:profile)",
            {"track_id": track_id, "now": now, **profile},
        )
        db.execute(
            "INSERT INTO playlists(kind,name,created_at,profile_id) VALUES('user','Transfer test',:now,:profile)",
            {"now": now, **profile},
        )

    report = transfer_sqlite_to_postgres(source, POSTGRES_URL)
    engine = engine_for(POSTGRES_URL, source)
    try:
        with engine.connect() as db:
            revision = db.execute(text("SELECT version_num FROM alembic_version")).scalar_one()
            profile_id = db.execute(
                text("SELECT value FROM installation_state WHERE key='legacy_profile_id'")
            ).scalar_one()
            history_profile = db.execute(
                text("SELECT profile_id FROM listening_history WHERE track_id=:track_id"),
                {"track_id": track_id},
            ).scalar_one()
            counts = tuple(
                db.execute(
                    text("SELECT (SELECT count(*) FROM tracks), (SELECT count(*) FROM listening_history), (SELECT count(*) FROM playlists)")
                ).one()
            )
    finally:
        engine.dispose()
    assert revision == f"musik_{LATEST_SCHEMA_VERSION}"
    assert history_profile == profile_id
    assert counts == (1, 1, 1)
    assert report["schema_version"] == LATEST_SCHEMA_VERSION
    assert len(report["tables"]) >= 40
