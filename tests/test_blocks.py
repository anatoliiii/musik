from datetime import datetime, timedelta, timezone

import numpy as np

from musik.brain import generators, mixes
from musik.brain.blocks import blocked_track_ids, track_is_blocked
from musik.brain.store import latest_playlist
from musik.config import get_settings
from musik.db.schema import connect, init_db, utcnow
from musik.index.brute import EmbeddingIndex


def test_artist_block_matches_normalized_name():
    rules = [("artist", "gone")]
    hidden = {"id": 11, "artist": " Gone ", "album": "A", "title": "Hidden", "file_md5": "abc", "cluster_id": 3}
    kept = {"id": 22, "artist": "Kept", "album": "B", "title": "Stay", "file_md5": "def", "cluster_id": 4}
    assert track_is_blocked(hidden, rules)
    assert not track_is_blocked(kept, rules)


def test_track_album_and_song_blocks():
    track = {"id": 11, "artist": "Gone", "album": "A", "title": "Hidden", "file_md5": "abc", "cluster_id": 3}
    assert track_is_blocked(track, [("track", "11")])
    assert track_is_blocked(track, [("album", "gone|a")])
    assert track_is_blocked(track, [("song", "abc")])
    assert track_is_blocked(track, [("song", "gone|hidden")])
    assert track_is_blocked(track, [("cluster", "3")])
    assert not track_is_blocked(track, [("artist", "other")])


def test_mix_pack_omits_blocked_artist(monkeypatch, tmp_path):
    monkeypatch.setenv("MUSIK_DB_PATH", str(tmp_path / "musik.db"))
    monkeypatch.setenv("MUSIK_DATA_DIR", str(tmp_path))
    get_settings.cache_clear()
    init_db()

    rng = np.random.default_rng(3)
    matrix = rng.normal(size=(12, 4)).astype(np.float32)
    matrix /= np.linalg.norm(matrix, axis=1, keepdims=True)
    meta = []
    for i in range(12):
        meta.append(
            {
                "id": i + 1,
                "artist": "Gone" if i < 4 else "Kept",
                "title": f"track-{i + 1}",
                "album": "Hide" if i < 4 else "Keep",
                "cluster_id": 1 if i < 4 else 2,
            }
        )
    index = EmbeddingIndex(
        track_ids=np.arange(1, 13, dtype=np.int64),
        matrix=matrix,
        meta=meta,
        md5s=[f"md5-{i}" for i in range(12)],
    )
    monkeypatch.setattr(mixes, "load_index", lambda: index)
    monkeypatch.setattr(generators, "load_index", lambda: index)

    now = datetime.now(timezone.utc).isoformat()
    with connect() as conn:
        for item in meta:
            conn.execute(
                """
                INSERT INTO tracks(id, path, title, artist, album, is_active, created_at, updated_at)
                VALUES (?, ?, ?, ?, ?, 1, ?, ?)
                """,
                (item["id"], f"/music/{item['id']}.flac", item["title"], item["artist"], item["album"], now, now),
            )
        conn.execute(
            """
            INSERT INTO radio_rules(
                rule_id, target_type, action, scope, target_key, strength, created_at
            ) VALUES ('block-gone', 'artist', 'block', 'global', 'gone', 1, ?)
            """,
            (utcnow(),),
        )
        conn.execute(
            """
            INSERT INTO user_profile_snapshots(context, embedding, created_at)
            VALUES ('global', ?, ?)
            """,
            (matrix[4].astype(np.float32).tobytes(), utcnow()),
        )

    assert blocked_track_ids() == {1, 2, 3, 4}

    mixes.generate_mix_pack(
        daily_size=3,
        for_you_size=3,
        weekday_size=3,
        weekly_size=3,
        new_size=3,
        reserve_prefix=1,
    )
    for kind in ("for_you", "daily", "weekly", "new_releases"):
        playlist = latest_playlist(kind)
        assert playlist is not None and playlist["tracks"], kind
        ids = {int(track["track_id"]) for track in playlist["tracks"]}
        assert ids.isdisjoint({1, 2, 3, 4}), (kind, ids)

    past = (datetime.now(timezone.utc) - timedelta(days=1)).isoformat()
    with connect() as conn:
        conn.execute("UPDATE radio_rules SET expires_at = ? WHERE rule_id = 'block-gone'", (past,))
    assert blocked_track_ids() == set()
    get_settings.cache_clear()
