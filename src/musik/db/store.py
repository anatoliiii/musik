from __future__ import annotations

from typing import Any

import numpy as np
from sqlalchemy import delete, func, literal, select

from musik.db.models import Feature, Genre, ScanState, Track, TrackGenre
from musik.db.schema import connect, init_db, utcnow


def ensure_db() -> None:
    init_db()


def _track_mapping(track: Track) -> dict[str, Any]:
    return {column.name: getattr(track, column.key) for column in Track.__table__.columns}


def upsert_track(data: dict[str, Any]) -> int:
    """Insert or update a catalog track through its SQLAlchemy mapping."""
    now = utcnow()
    fields = (
        "file_md5", "file_mtime", "file_size", "title", "artist", "album",
        "year", "track_number", "duration", "bitrate", "sample_rate",
        "channels", "fingerprint", "lufs", "artwork_path",
    )
    values = {name: data.get(name) for name in fields}
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


DUPLICATE_DURATION_TOLERANCE_SEC = 3.0


def mark_duplicates() -> int:
    """Mark duplicate catalog files by hash, fingerprint, then metadata."""
    with connect() as conn:
        session = conn.session
        session.query(Track).filter(
            Track.is_active == 1, Track.is_duplicate_of.is_not(None)
        ).update({Track.is_duplicate_of: None}, synchronize_session="fetch")
        marked = 0

        def mark_by(field: str) -> int:
            column = getattr(Track, field)
            tracks = session.scalars(
                select(Track)
                .where(
                    Track.is_active == 1,
                    Track.is_duplicate_of.is_(None),
                    column.is_not(None),
                    column != "",
                )
                .order_by(
                    column,
                    func.coalesce(Track.bitrate, 0).desc(),
                    func.coalesce(Track.file_size, 0).desc(),
                    Track.id.asc(),
                )
            ).all()
            best_by_key: dict[str, int] = {}
            duplicates = 0
            now = utcnow()
            for track in tracks:
                key = getattr(track, field)
                if key not in best_by_key:
                    best_by_key[key] = track.id
                else:
                    track.is_duplicate_of = best_by_key[key]
                    track.updated_at = now
                    duplicates += 1
            return duplicates

        marked += mark_by("file_md5")
        marked += mark_by("fingerprint")

        normalized_artist = func.lower(func.trim(Track.artist))
        normalized_title = func.lower(func.trim(Track.title))
        group_key = normalized_artist + literal("|") + normalized_title
        metadata_rows = session.execute(
            select(Track, group_key.label("group_key"))
            .where(
                Track.is_active == 1,
                Track.is_duplicate_of.is_(None),
                func.trim(func.coalesce(Track.artist, "")) != "",
                func.trim(func.coalesce(Track.title, "")) != "",
                Track.duration.is_not(None),
                Track.duration > 0,
            )
            .order_by(
                normalized_artist,
                normalized_title,
                func.coalesce(Track.bitrate, 0).desc(),
                func.coalesce(Track.file_size, 0).desc(),
                Track.id.asc(),
            )
        ).all()
        kept: dict[str, list[tuple[int, float]]] = {}
        now = utcnow()
        for track, key in metadata_rows:
            copies = kept.setdefault(key, [])
            best = next(
                (track_id for track_id, duration in copies
                 if abs(duration - track.duration) <= DUPLICATE_DURATION_TOLERANCE_SEC),
                None,
            )
            if best is None:
                copies.append((track.id, track.duration))
            else:
                track.is_duplicate_of = best
                track.updated_at = now
                marked += 1
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


def get_embedding(track_id: int) -> np.ndarray | None:
    with connect() as conn:
        feature = conn.session.get(Feature, track_id)
        if feature is None or feature.embedding is None:
            return None
        dim = int(feature.embedding_dim or 0)
        arr = np.frombuffer(feature.embedding, dtype=np.float32)
        if dim and arr.size != dim:
            return arr.astype(np.float32)
        return np.asarray(arr, dtype=np.float32)
