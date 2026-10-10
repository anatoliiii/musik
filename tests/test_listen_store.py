"""Upsert paths of the worker's listening repositories on the selected backend."""
from __future__ import annotations

from pathlib import Path

from musik.db.schema import connect, init_db, utcnow
from musik.listen.history import bump_rec_stats, record_listen, top_transitions


def _tracks(n: int) -> None:
    now = utcnow()
    with connect() as conn:
        for track_id in range(1, n + 1):
            conn.execute(
                "INSERT INTO tracks(id, path, title, artist, created_at, updated_at) "
                "VALUES (:id, :path, 'T', 'A', :now, :now)",
                {"id": track_id, "path": f"/listen/{track_id}.flac", "now": now},
            )


def test_repeated_listens_accumulate_through_conflict_updates(tmp_path: Path, test_database) -> None:
    test_database(tmp_path / "musik.db")
    init_db()
    _tracks(2)

    first = record_listen(1, "start", source="test")
    for _ in range(2):
        record_listen(2, "finish", source="test", prev_track_id=1, transition_weight=1.5)
        bump_rec_stats(2, shown=1, completed=1)

    assert first > 0
    assert [(t["from_id"], t["to_id"], t["weight"]) for t in top_transitions()] == [(1, 2, 3.0)]
    with connect() as conn:
        stats = conn.execute(
            "SELECT shown, completed FROM rec_stats WHERE profile_id = :profile AND track_id = 2",
            {"profile": conn.profile_id},
        ).fetchone()
    assert (stats["shown"], stats["completed"]) == (2, 2)
