from __future__ import annotations

from pathlib import Path

from musik.db import ensure_db, mark_duplicates
from musik.db.schema import connect, utcnow


def _setup_db(tmp_path: Path, monkeypatch, test_database) -> None:
    monkeypatch.setenv("MUSIK_LIBRARY", str(tmp_path / "lib"))
    monkeypatch.setenv("MUSIK_DATA_DIR", str(tmp_path / "data"))
    test_database(tmp_path / "data" / "db" / "t.db")
    monkeypatch.setenv("MUSIK_EMBEDDINGS_CACHE", str(tmp_path / "data" / "cache" / "embeddings"))
    monkeypatch.setenv("MUSIK_ARTWORK_CACHE", str(tmp_path / "data" / "cache" / "artwork"))

    from musik.config import get_settings

    get_settings.cache_clear()
    ensure_db()


def _add(conn, path: str, duration: float, bitrate: int = 320) -> int:
    return conn.execute(
        """
        INSERT INTO tracks (path, file_md5, file_size, title, artist, duration, bitrate,
                            is_active, created_at, updated_at)
        VALUES (:path, :path, 1000, 'Song One', 'Artist A', :duration, :bitrate, 1, :now, :now)
        RETURNING id
        """,
        {"path": path, "duration": duration, "bitrate": bitrate, "now": utcnow()},
    ).scalar_one()


def test_same_title_different_takes_are_kept(tmp_path: Path, monkeypatch, test_database):
    _setup_db(tmp_path, monkeypatch, test_database)
    with connect() as conn:
        studio = _add(conn, "/lib/studio/song-one.mp3", 93.0)
        live = _add(conn, "/lib/live/song-one.mp3", 162.0)

    assert mark_duplicates() == 0
    with connect() as conn:
        dup = dict(conn.execute("SELECT id, is_duplicate_of FROM tracks").fetchall())
    assert dup[studio] is None and dup[live] is None


def test_same_title_same_length_is_a_duplicate(tmp_path: Path, monkeypatch, test_database):
    _setup_db(tmp_path, monkeypatch, test_database)
    with connect() as conn:
        best = _add(conn, "/lib/a/song-one.flac", 162.0, bitrate=900)
        reencode = _add(conn, "/lib/b/song-one.mp3", 163.5, bitrate=192)
        live = _add(conn, "/lib/c/song-one.mp3", 93.0)

    assert mark_duplicates() == 1
    with connect() as conn:
        dup = dict(conn.execute("SELECT id, is_duplicate_of FROM tracks").fetchall())
    assert dup[reencode] == best
    assert dup[best] is None and dup[live] is None
