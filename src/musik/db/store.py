from __future__ import annotations

import json
from typing import Any

import numpy as np
from sqlalchemy import delete, func, select

from musik.db.models import Feature, Genre, ScanState, Track, TrackGenre
from musik.db.schema import connect, init_db, utcnow


def ensure_db() -> None:
    init_db()


def _track_mapping(track: Track) -> dict[str, Any]:
    return {column.name: getattr(track, column.key) for column in Track.__table__.columns}


# Bump when the scanner starts reading or deriving more from tags; the next
# scan then re-reads tags of unchanged files once (no audio analysis).
TAG_SCHEMA_VERSION = 2

TAG_FIELDS = ("title", "artist", "album", "year", "track_number")

# Who an album belongs to: the album-level credit, "Various Artists" for a
# flagged compilation without one, otherwise the track artist. The Go player
# groups albums by the same rule (db.albumOwnerSQL).
VARIOUS_ARTISTS = "Various Artists"
ALBUM_OWNER_SQL = (
    "COALESCE(NULLIF(trim(t.album_artist), ''), "
    f"CASE WHEN t.is_compilation = 1 THEN '{VARIOUS_ARTISTS}' END, t.artist)"
)


def _derived_tag_values(data: dict[str, Any]) -> dict[str, Any]:
    album_artist = (data.get("album_artist") or "").strip() or None
    return {
        "is_remaster": 1 if data.get("is_remaster") else 0,
        "artist_segments": json.dumps(
            [str(segment) for segment in (data.get("artist_segments") or [])], ensure_ascii=False
        ),
        "album_artist": album_artist,
        "is_compilation": 1 if data.get("is_compilation") else 0,
    }


def update_track_tags(path: str, data: dict[str, Any]) -> bool:
    """Refresh tag-derived columns of an unchanged file without re-analysis."""
    with connect() as conn:
        track = conn.session.scalar(select(Track).where(Track.path == path))
        if track is None:
            return False
        values = {name: data.get(name) for name in TAG_FIELDS}
        values.update(_derived_tag_values(data))
        for name, value in values.items():
            setattr(track, name, value)
        track.updated_at = utcnow()
        return True


def tag_schema_version() -> int:
    with connect() as conn:
        state = conn.session.get(ScanState, "tag_schema_version")
        return int(state.value) if state is not None and str(state.value).isdigit() else 1


def set_tag_schema_version(version: int) -> None:
    with connect() as conn:
        state = conn.session.get(ScanState, "tag_schema_version")
        if state is None:
            conn.session.add(ScanState(key="tag_schema_version", value=str(version)))
        else:
            state.value = str(version)


def upsert_track(data: dict[str, Any]) -> int:
    """Insert or update a catalog track through its SQLAlchemy mapping."""
    now = utcnow()
    fields = (
        "file_md5", "file_mtime", "file_size", "title", "artist", "album",
        "year", "track_number", "duration", "bitrate", "sample_rate",
        "channels", "fingerprint", "lufs", "artwork_path",
    )
    values = {name: data.get(name) for name in fields}
    values.update(_derived_tag_values(data))
    with connect() as conn:
        session = conn.session
        track = session.scalar(select(Track).where(Track.path == data["path"]))
        if track is None:
            track = Track(
                path=data["path"], **values, is_active=1,
                created_at=now, updated_at=now,
            )
            session.add(track)
            session.flush()
        else:
            for name, value in values.items():
                setattr(track, name, value)
            track.is_active = 1
            track.updated_at = now

        feature = session.get(Feature, track.id)
        if feature is None:
            session.add(Feature(track_id=track.id, status="pending"))
        return int(track.id)


def set_genres(track_id: int, genre_names: list[str]) -> None:
    with connect() as conn:
        session = conn.session
        session.execute(delete(TrackGenre).where(TrackGenre.track_id == track_id))
        added: set[str] = set()
        for raw_name in genre_names:
            name = raw_name.strip()
            if not name or name in added:
                continue
            added.add(name)
            genre = session.scalar(select(Genre).where(Genre.name == name))
            if genre is None:
                genre = Genre(name=name)
                session.add(genre)
                session.flush()
            session.add(TrackGenre(track_id=track_id, genre_id=genre.id))


def mark_missing_inactive(seen_paths: set[str]) -> int:
    with connect() as conn:
        tracks = conn.session.scalars(
            select(Track).where(Track.is_active == 1)
        ).all()
        now = utcnow()
        missing = [track for track in tracks if track.path not in seen_paths]
        for track in missing:
            track.is_active = 0
            track.updated_at = now
        return len(missing)


def update_fingerprint_and_lufs(
    track_id: int, *, fingerprint: str | None, lufs: float | None
) -> None:
    with connect() as conn:
        track = conn.session.get(Track, track_id)
        if track is not None:
            if fingerprint is not None:
                track.fingerprint = fingerprint
            if lufs is not None:
                track.lufs = lufs
            track.updated_at = utcnow()
        if lufs is not None:
            feature = conn.session.get(Feature, track_id)
            if feature is not None:
                feature.lufs = lufs


def update_audio_scalars(
    track_id: int,
    *,
    bpm: float | None = None,
    key_name: str | None = None,
    mode: str | None = None,
    lufs: float | None = None,
) -> None:
    with connect() as conn:
        feature = conn.session.get(Feature, track_id)
        if feature is not None:
            if bpm is not None:
                feature.bpm = bpm
            if key_name is not None:
                feature.key_name = key_name
            if mode is not None:
                feature.mode = mode
            if lufs is not None:
                feature.lufs = lufs
        if lufs is not None:
            track = conn.session.get(Track, track_id)
            if track is not None:
                track.lufs = lufs
                track.updated_at = utcnow()


# First tie-breaker inside a duplicate group: container/format rank, lower wins.
# FLAC beats WAV/AIFF beats Opus/OGG beats M4A/AAC beats MP3 beats anything else.
# Lower rank wins a duplicate group: lossless first, then by codec quality.
FORMAT_RANK = {".flac": 0, ".wav": 1, ".aiff": 1, ".aif": 1, ".opus": 2, ".ogg": 2,
               ".m4a": 3, ".aac": 3, ".mp3": 4}

# Copies whose durations differ by more than this are different takes
# (live vs studio), even when artist, title and album agree.
DUPLICATE_DURATION_TOLERANCE_SEC = 3.0


def _format_rank(path: str) -> int:
    lower = path.lower()
    return next((rank for ext, rank in FORMAT_RANK.items() if lower.endswith(ext)), 5)


def _norm(value: str | None) -> str:
    return (value or "").strip().lower()


def mark_duplicates() -> int:
    """Mark duplicates: same MD5, then fingerprint, then artist+title+album.

    The winner of a group is the finest copy: format rank first (FLAC >
    WAV/AIFF > Opus/OGG > M4A/AAC > MP3 > other), then highest bitrate,
    then largest file (stable: lowest id). Album, year and the remaster flag
    keep originals, remasters and other editions apart, and durations must
    agree within DUPLICATE_DURATION_TOLERANCE_SEC so live and studio takes
    that share one title never merge. Everyone else gets is_duplicate_of.
    """
    with connect() as conn:
        session = conn.session
        session.query(Track).filter(
            Track.is_active == 1, Track.is_duplicate_of.is_not(None)
        ).update({Track.is_duplicate_of: None}, synchronize_session="fetch")
        tracks = sorted(
            session.scalars(select(Track).where(Track.is_active == 1)).all(),
            key=lambda t: (_format_rank(t.path), -(t.bitrate or 0), -(t.file_size or 0), t.id),
        )
        now = utcnow()
        marked = 0

        def edition(track: Track) -> str:
            return f"{track.year or ''}|{track.is_remaster or 0}"

        def mark(key_of, *, tolerance: bool = False) -> int:
            kept: dict[str, list[Track]] = {}
            duplicates = 0
            for track in tracks:
                if track.is_duplicate_of is not None:
                    continue
                key = key_of(track)
                if not key:
                    continue
                copies = kept.setdefault(key, [])
                best = next(
                    (c for c in copies
                     if not tolerance
                     or abs((c.duration or 0) - (track.duration or 0)) <= DUPLICATE_DURATION_TOLERANCE_SEC),
                    None,
                )
                if best is None:
                    copies.append(track)
                else:
                    track.is_duplicate_of = best.id
                    track.updated_at = now
                    duplicates += 1
            return duplicates

        # 1) identical files
        marked += mark(lambda t: t.file_md5 or "")
        # 2) chromaprint; year and remaster flag keep editions apart even when
        # the fingerprint survives mastering.
        marked += mark(lambda t: f"{t.fingerprint}|{edition(t)}" if t.fingerprint else "")
        # 3) same song metadata (different encodes / renames / editions)
        marked += mark(
            lambda t: (
                f"{_norm(t.artist)}|{_norm(t.title)}|{_norm(t.album)}|{edition(t)}"
                if _norm(t.artist) and _norm(t.title) and (t.duration or 0) > 0 else ""
            ),
            tolerance=True,
        )
        return marked


def counts() -> dict[str, int]:
    with connect() as conn:
        session = conn.session
        total = session.scalar(select(func.count()).select_from(Track)) or 0
        active = session.scalar(
            select(func.count()).select_from(Track).where(
                Track.is_active == 1, Track.is_duplicate_of.is_(None)
            )
        ) or 0
        pending = session.scalar(
            select(func.count()).select_from(Feature).where(
                Feature.status.in_(("pending", "retry"))
            )
        ) or 0
        ready = session.scalar(
            select(func.count()).select_from(Feature).where(Feature.status == "ready")
        ) or 0
        failed = session.scalar(
            select(func.count()).select_from(Feature).where(Feature.status == "failed")
        ) or 0
        return {
            "tracks_total": int(total),
            "tracks_active": int(active),
            "features_pending": int(pending),
            "features_ready": int(ready),
            "features_failed": int(failed),
        }


def list_active_tracks(limit: int = 20) -> list[dict[str, Any]]:
    with connect() as conn:
        statement = (
            select(
                Track.id, Track.title, Track.artist, Track.album, Track.path,
                Track.bitrate, Track.lufs, Track.fingerprint,
            )
            .where(Track.is_active == 1, Track.is_duplicate_of.is_(None))
            .order_by(Track.artist, Track.album, Track.track_number)
        )
        if int(limit) >= 0:
            statement = statement.limit(int(limit))
        rows = conn.session.execute(statement).all()
        return [dict(row._mapping) for row in rows]


def get_track_by_path(path: str) -> dict[str, Any] | None:
    with connect() as conn:
        track = conn.session.scalar(select(Track).where(Track.path == path))
        return _track_mapping(track) if track is not None else None


def track_file_states() -> dict[str, dict[str, Any]]:
    """Return the lightweight file state needed by incremental scans."""
    with connect() as conn:
        rows = conn.session.execute(
            select(Track.path, Track.file_mtime, Track.file_size, Track.is_active)
        ).all()
        return {str(row.path): dict(row._mapping) for row in rows}


def list_tracks_needing_embedding(
    *, limit: int | None = None, force: bool = False
) -> list[dict[str, Any]]:
    """Active non-duplicate tracks without a ready embedding (or all if force)."""
    statement = (
        select(
            Track.id.label("id"), Track.path.label("path"),
            Track.file_md5.label("file_md5"), Track.duration.label("duration"),
            Track.title.label("title"), Track.artist.label("artist"),
            Feature.status.label("status"),
        )
        .join(Feature, Feature.track_id == Track.id)
        .where(Track.is_active == 1, Track.is_duplicate_of.is_(None))
    )
    if not force:
        statement = statement.where(
            (Feature.status != "ready") | Feature.embedding.is_(None)
        )
    statement = statement.order_by(Track.artist, Track.album, Track.track_number)
    if limit is not None and int(limit) >= 0:
        statement = statement.limit(int(limit))
    with connect() as conn:
        rows = conn.session.execute(statement).all()
        return [dict(row._mapping) for row in rows]


def save_embedding(track_id: int, embedding: np.ndarray, *, model_id: str | None = None) -> None:
    vec = np.asarray(embedding, dtype=np.float32).reshape(-1)
    blob = vec.tobytes()
    now = utcnow()
    with connect() as conn:
        session = conn.session
        feature = session.get(Feature, track_id)
        if feature is not None:
            feature.embedding = blob
            feature.embedding_dim = int(vec.shape[0])
            feature.status = "ready"
            feature.error = None
            feature.computed_at = now
        if model_id:
            state = session.get(ScanState, "clap_model")
            if state is None:
                session.add(ScanState(key="clap_model", value=model_id))
            else:
                state.value = model_id


def mark_feature_failed(track_id: int, error: str) -> None:
    with connect() as conn:
        feature = conn.session.get(Feature, track_id)
        if feature is not None:
            feature.status = "failed"
            feature.error = error[:2000]
            feature.computed_at = utcnow()


def list_tracks_needing_artwork(
    *, limit: int | None = None, force: bool = False
) -> list[dict[str, Any]]:
    """Active non-duplicate tracks with an album to look up.

    The caller checks the artwork file itself: a stored ``artwork_path`` may be
    empty, point at a container path, or reference a file that was deleted.
    Both cases ("empty path" or "file missing") are handled in Python, so the
    query never discards a row because it happens to carry a non-empty string.
    """
    sql = """
        SELECT t.id, t.path, t.file_md5, """ + ALBUM_OWNER_SQL + """ AS artist, t.album, t.artwork_path
        FROM tracks t
        WHERE t.is_active = 1 AND COALESCE(t.is_duplicate_of, 0) = 0
          AND trim(COALESCE(t.artist, '')) != ''
          AND trim(COALESCE(t.album, '')) != ''
          AND t.file_md5 IS NOT NULL AND t.file_md5 != ''
    """
    sql += " ORDER BY t.artist, t.album, t.track_number"
    if limit is not None:
        sql += f" LIMIT {int(limit)}"
    with connect() as conn:
        return [dict(r) for r in conn.execute(sql).fetchall()]


def recent_artwork_misses(since_iso: str) -> set[str]:
    """Album keys looked up without a match on or after since_iso."""
    with connect() as conn:
        rows = conn.execute(
            "SELECT album_key FROM artwork_lookups WHERE status = 'missing' AND checked_at >= :since",
            {"since": since_iso},
        ).fetchall()
    return {str(r[0]) for r in rows}


def record_artwork_lookup(album_key: str, found: bool) -> None:
    with connect() as conn:
        if found:
            conn.execute("DELETE FROM artwork_lookups WHERE album_key = :key", {"key": album_key})
        else:
            conn.execute(
                """
                INSERT INTO artwork_lookups(album_key, status, checked_at)
                VALUES (:key, 'missing', :now)
                ON CONFLICT(album_key) DO UPDATE SET
                  status = excluded.status, checked_at = excluded.checked_at
                """,
                {"key": album_key, "now": utcnow()},
            )


def list_library_artists() -> list[str]:
    """Distinct artist names as the library shows them (collaborator segments)."""
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT artist, artist_segments FROM tracks
            WHERE is_active = 1 AND COALESCE(is_duplicate_of, 0) = 0
            """
        ).fetchall()
    seen: dict[str, str] = {}
    for artist, segments_json in rows:
        try:
            segments = json.loads(segments_json) if segments_json else []
        except ValueError:
            segments = []
        names = [str(s) for s in segments if str(s).strip()] or [str(artist or "")]
        for name in names:
            name = " ".join(name.split())
            if name:
                seen.setdefault(name.lower(), name)
    return sorted(seen.values(), key=str.lower)


def artist_photo_states() -> dict[str, dict[str, Any]]:
    """name_key -> {status, path, checked_at} for every stored lookup."""
    with connect() as conn:
        rows = conn.execute(
            "SELECT name_key, status, path, checked_at FROM artist_photos"
        ).fetchall()
    return {
        str(r[0]): {"status": r[1], "path": r[2], "checked_at": r[3]} for r in rows
    }


def save_artist_photo(name_key: str, artist: str, path: str | None, source: str) -> None:
    status = "found" if path else "missing"
    with connect() as conn:
        conn.execute(
            """
            INSERT INTO artist_photos(name_key, artist, path, status, source, checked_at)
            VALUES (:name_key, :artist, :path, :status, :source, :now)
            ON CONFLICT(name_key) DO UPDATE SET
              artist = excluded.artist, path = excluded.path, status = excluded.status,
              source = excluded.source, checked_at = excluded.checked_at
            """,
            {"name_key": name_key, "artist": artist, "path": path, "status": status,
             "source": source, "now": utcnow()},
        )


def save_artwork_path(track_id: int, path: str) -> None:
    with connect() as conn:
        conn.execute(
            "UPDATE tracks SET artwork_path = :path, updated_at = :now WHERE id = :id",
            {"path": path, "now": utcnow(), "id": track_id},
        )
