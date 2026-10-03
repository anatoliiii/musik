"""Catalog repository ORM behavior on PostgreSQL 17."""
from __future__ import annotations

import os
import uuid
from pathlib import Path

import numpy as np
import pytest
from sqlalchemy import delete, select, update
from sqlalchemy.orm import Session

from musik.config import get_settings
from musik.db.models import Feature, Genre, Track, TrackGenre
from musik.db.orm import engine_for
from musik.db.store import (
    get_embedding,
    get_track_by_path,
    list_tracks_needing_embedding,
    mark_duplicates,
    save_embedding,
    set_genres,
    upsert_track,
)


POSTGRES_URL = os.environ.get("MUSIK_TEST_POSTGRES_URL")
pytestmark = pytest.mark.skipif(not POSTGRES_URL, reason="requires a disposable PostgreSQL test database")


def test_catalog_repository_uses_mapped_orm_on_postgres(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    assert POSTGRES_URL is not None
    monkeypatch.setenv("MUSIK_DATABASE_URL", POSTGRES_URL)
    monkeypatch.delenv("MUSIK_DB_PATH", raising=False)
    get_settings.cache_clear()

    suffix = uuid.uuid4().hex
    paths = (f"/orm-test/{suffix}-best.flac", f"/orm-test/{suffix}-copy.flac")
    genre_name = f"orm-test-{suffix}"
    first_id = second_id = None
    try:
        common = {
            "file_md5": f"md5-{suffix}", "file_size": 1000,
            "title": f"ORM test {suffix}", "artist": "ORM test artist",
            "album": "ORM test album", "duration": 180.0,
        }
        first_id = upsert_track({**common, "path": paths[0], "bitrate": 320})
        second_id = upsert_track({**common, "path": paths[1], "bitrate": 128})
        set_genres(first_id, [genre_name, genre_name])
        expected = np.asarray([0.25, -0.5, 0.75], dtype=np.float32)
        save_embedding(first_id, expected, model_id=f"model-{suffix}")

        assert mark_duplicates() >= 1
        first = get_track_by_path(paths[0])
        second = get_track_by_path(paths[1])
        assert first is not None and first["id"] == first_id
        assert second is not None and second["is_duplicate_of"] == first_id
        np.testing.assert_array_equal(get_embedding(first_id), expected)
        pending = list_tracks_needing_embedding()
        assert first_id not in {row["id"] for row in pending}
        assert second_id not in {row["id"] for row in pending}
    finally:
        engine = engine_for(POSTGRES_URL, tmp_path / "unused.sqlite")
        with Session(engine) as session, session.begin():
            tracks = session.scalars(select(Track).where(Track.path.in_(paths))).all()
            track_ids = [track.id for track in tracks]
            if track_ids:
                session.execute(
                    update(Track).where(Track.id.in_(track_ids)).values(is_duplicate_of=None)
                )
                session.execute(delete(TrackGenre).where(TrackGenre.track_id.in_(track_ids)))
                session.execute(delete(Feature).where(Feature.track_id.in_(track_ids)))
                session.execute(delete(Track).where(Track.id.in_(track_ids)))
            session.execute(delete(Genre).where(Genre.name == genre_name))
        get_settings.cache_clear()
