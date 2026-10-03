"""Disposable PostgreSQL 17 migration and SQLite transfer integration test."""
from __future__ import annotations

import os
from pathlib import Path

import pytest
from sqlalchemy import text

from musik.db.migrations import migrate_database
from musik.db.orm import engine_for
from musik.db.schema import connect, utcnow
from musik.db.transfer import transfer_sqlite_to_postgres


POSTGRES_URL = os.environ.get("MUSIK_TEST_POSTGRES_URL")
pytestmark = pytest.mark.skipif(not POSTGRES_URL, reason="requires a disposable PostgreSQL test database")


def test_postgres_migration_and_verified_sqlite_transfer(tmp_path: Path) -> None:
    assert POSTGRES_URL is not None
    migrate_database(POSTGRES_URL, tmp_path / "unused.sqlite")
    source = tmp_path / "source.sqlite"
    migrate_database(None, source)
    now = utcnow()
    with connect(source) as db:
        track_id = db.execute(
            """INSERT INTO tracks(path,title,artist,album,is_active,created_at,updated_at)
               VALUES(?,?,?,?,1,?,?)""",
            ("/test/transfer.flac", "Transfer", "Test", "Album", now, now),
        ).lastrowid
        db.execute(
            "INSERT INTO listening_history(track_id,ts,action) VALUES(?,?,'finish')",
            (track_id, now),
        )
        db.execute("INSERT INTO playlists(kind,name,created_at) VALUES('user','Transfer test',?)", (now,))

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
    assert revision == "musik_7"
    assert history_profile == profile_id
    assert counts == (1, 1, 1)
    assert report["schema_version"] == 7
    assert len(report["tables"]) >= 40
