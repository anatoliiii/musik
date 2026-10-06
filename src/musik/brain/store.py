"""Persist generated playlists in the configured database."""

from __future__ import annotations

import json
from typing import Any
from uuid import uuid4

from musik.db.schema import connect, utcnow


def save_playlist(
    *,
    kind: str,
    name: str,
    entries: list[dict[str, Any]],
    meta: dict[str, Any] | None = None,
    retain: int = 14,
) -> int:
    """
    entries: [{track_id, explanation}, ...] in order.
    Returns playlist id.
    """
    now = utcnow()
    with connect() as conn:
        profile = conn.profile_id
        pid = int(conn.execute(
            """INSERT INTO playlists(kind, name, created_at, meta_json, type, updated_at, profile_id)
               VALUES (:kind, :name, :now, :meta, 'generated', :now, :profile)
               RETURNING id""",
            {"kind": kind, "name": name, "now": now,
             "meta": json.dumps(meta or {}, ensure_ascii=False), "profile": profile},
        ).scalar_one())
        for pos, e in enumerate(entries):
            conn.execute(
                """
                INSERT INTO playlist_tracks(
                    item_id, playlist_id, position, track_id, added_at, source, explanation, profile_id)
                VALUES (:item, :playlist, :pos, :track, :now, 'rule', :explanation, :profile)
                """,
                {"item": uuid4().hex, "playlist": pid, "pos": pos, "track": int(e["track_id"]),
                 "now": now, "explanation": e.get("explanation"), "profile": profile},
            )
        if retain > 0:
            conn.execute(
                """
                DELETE FROM playlists
                WHERE profile_id = :profile AND kind = :kind AND id NOT IN (
                    SELECT id FROM playlists
                    WHERE profile_id = :profile AND kind = :kind
                    ORDER BY id DESC
                    LIMIT :retain
                )
                """,
                {"profile": profile, "kind": kind, "retain": retain},
            )
        return pid


def list_playlists(limit: int = 30) -> list[dict[str, Any]]:
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT p.id, p.kind, p.name, p.created_at,
                   (SELECT COUNT(*) FROM playlist_tracks pt
                    WHERE pt.profile_id = p.profile_id AND pt.playlist_id = p.id) AS n
            FROM playlists p
            WHERE p.profile_id = :profile
            ORDER BY p.id DESC
            LIMIT :limit
            """,
            {"profile": conn.profile_id, "limit": limit},
        ).fetchall()
        return [dict(r) for r in rows]


def latest_playlist(kind: str) -> dict[str, Any] | None:
    """Most recent saved playlist of the given kind (e.g. daily)."""
    with connect() as conn:
        row = conn.execute(
            """
            SELECT id FROM playlists
            WHERE profile_id = :profile AND kind = :kind
            ORDER BY id DESC
            LIMIT 1
            """,
            {"profile": conn.profile_id, "kind": kind},
        ).fetchone()
    if not row:
        return None
    return get_playlist(int(row["id"]))


def get_playlist(playlist_id: int) -> dict[str, Any] | None:
    with connect() as conn:
        row = conn.execute(
            "SELECT id, kind, name, created_at, meta_json FROM playlists WHERE profile_id = :profile AND id = :id",
            {"profile": conn.profile_id, "id": playlist_id},
        ).fetchone()
        if not row:
            return None
        pl = dict(row)
        tracks = conn.execute(
            """
            SELECT pt.position, pt.track_id, pt.explanation,
                   t.artist, t.title, t.path
            FROM playlist_tracks pt
            JOIN tracks t ON t.id = pt.track_id
            WHERE pt.profile_id = :profile AND pt.playlist_id = :id
            ORDER BY pt.position
            """,
            {"profile": conn.profile_id, "id": playlist_id},
        ).fetchall()
        pl["tracks"] = [dict(t) for t in tracks]
        return pl
