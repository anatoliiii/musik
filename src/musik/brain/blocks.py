"""Active global block rules, shared by mix generation."""

from __future__ import annotations

from musik.db.schema import connect, utcnow


def _norm(value: str | None) -> str:
    return (value or "").strip().lower()


def track_is_blocked(track: dict, rules: list[tuple[str, str]]) -> bool:
    """True when a global block rule matches this track.

    Keys follow the player: artist and album names are trimmed and lowercased,
    album is ``artist|album``, song is ``artist|title`` or the file md5.
    """
    artist = _norm(track.get("artist"))
    album = _norm(track.get("album"))
    title = _norm(track.get("title"))
    md5 = track.get("file_md5") or ""
    track_id = str(track.get("id") or "")
    cluster = "" if track.get("cluster_id") is None else str(track.get("cluster_id"))
    song = f"{artist}|{title}" if artist and title else ""
    album_key = f"{artist}|{album}" if album else ""
    for target_type, key in rules:
        if target_type == "track" and key == track_id:
            return True
        if target_type == "artist" and key == artist and artist:
            return True
        if target_type == "album" and album_key and key == album_key:
            return True
        if target_type == "song" and key and (key == md5 or key == song):
            return True
        if target_type == "cluster" and cluster and key == cluster:
            return True
    return False


def blocked_track_ids() -> set[int]:
    """Track ids hidden by a current global block rule. Empty if the table is missing."""
    now = utcnow()
    try:
        with connect() as conn:
            rules = conn.execute(
                """
                SELECT target_type, target_key
                FROM radio_rules
                WHERE action = 'block'
                  AND scope = 'global'
                  AND archived_at IS NULL
                  AND (expires_at IS NULL OR expires_at = '' OR expires_at > ?)
                """,
                (now,),
            ).fetchall()
            if not rules:
                return set()
            rows = conn.execute(
                """
                SELECT t.id,
                       COALESCE(t.artist,'') AS artist,
                       COALESCE(t.album,'') AS album,
                       COALESCE(t.title,'') AS title,
                       COALESCE(t.file_md5,'') AS file_md5,
                       f.cluster_id
                FROM tracks t
                LEFT JOIN features f ON f.track_id = t.id
                WHERE t.is_active = 1
                """
            ).fetchall()
    except Exception:
        return set()
    parsed = [(str(rule["target_type"]), str(rule["target_key"])) for rule in rules]
    out: set[int] = set()
    for row in rows:
        track = {
            "id": row["id"],
            "artist": row["artist"],
            "album": row["album"],
            "title": row["title"],
            "file_md5": row["file_md5"],
            "cluster_id": row["cluster_id"],
        }
        if track_is_blocked(track, parsed):
            out.add(int(row["id"]))
    return out
