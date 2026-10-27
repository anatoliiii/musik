from __future__ import annotations

import numpy as np

from musik.db import ensure_db, save_embedding, upsert_track
from musik.db.schema import connect
from musik.index.brute import load_index
from musik.privacy import private_owner, visible_to


def test_private_owner_matches_go_rule():
    assert private_owner("/music/Band/a.flac") == ""
    assert private_owner("/music/_private/p1/Yandex Music/a.flac") == "p1"
    assert private_owner("/other/mount/_private/p2/a.mp3") == "p2"
    assert private_owner("/music/my_private/p1/a.mp3") == ""
    assert private_owner("/music/_private/") == ""
    assert private_owner(r"C:\music\_private\p3\a.mp3") == "p3"
    assert visible_to("/music/a.flac", None)
    assert visible_to("/music/_private/p1/a.flac", "p1")
    assert not visible_to("/music/_private/p1/a.flac", "p2")
    assert not visible_to("/music/_private/p1/a.flac", "")


def test_load_index_skips_other_profiles_private_tracks(tmp_path, monkeypatch, test_database):
    monkeypatch.setenv("MUSIK_DATA_DIR", str(tmp_path / "data"))
    test_database(tmp_path / "data" / "db" / "t.db")
    from musik.config import get_settings

    get_settings.cache_clear()
    ensure_db()
    with connect() as conn:
        me = conn.profile_id
    paths = {
        "shared": tmp_path / "music" / "a.flac",
        "mine": tmp_path / "music" / "_private" / me / "b.flac",
        "theirs": tmp_path / "music" / "_private" / "someone-else" / "c.flac",
    }
    ids = {}
    rng = np.random.default_rng(0)
    for i, (name, path) in enumerate(paths.items()):
        ids[name] = upsert_track(
            {
                "path": str(path), "file_md5": f"md5-{i}", "file_mtime": 0.0, "file_size": 1,
                "title": name, "artist": "A", "album": None, "year": None, "track_number": i,
                "duration": 1.0, "bitrate": 128, "sample_rate": 44100, "channels": 2,
                "fingerprint": None, "lufs": None, "artwork_path": None,
            }
        )
        vec = rng.normal(size=8).astype(np.float32)
        save_embedding(ids[name], vec / np.linalg.norm(vec))

    loaded = set(int(t) for t in load_index().track_ids)
    assert loaded == {ids["shared"], ids["mine"]}
