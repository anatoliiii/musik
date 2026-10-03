"""SQLAlchemy mappings for the shared musik relational schema."""
from __future__ import annotations

from sqlalchemy import ForeignKey, Integer, LargeBinary, String, Text, UniqueConstraint
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column


class Base(DeclarativeBase):
    pass


class Track(Base):
    __tablename__ = "tracks"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    path: Mapped[str] = mapped_column(Text, unique=True, nullable=False)
    file_md5: Mapped[str | None] = mapped_column(Text)
    file_mtime: Mapped[float | None]
    file_size: Mapped[int | None]
    title: Mapped[str | None] = mapped_column(Text)
    artist: Mapped[str | None] = mapped_column(Text)
    album: Mapped[str | None] = mapped_column(Text)
    year: Mapped[int | None]
    track_number: Mapped[int | None]
    duration: Mapped[float | None]
    bitrate: Mapped[int | None]
    sample_rate: Mapped[int | None]
    channels: Mapped[int | None]
    fingerprint: Mapped[str | None] = mapped_column(Text)
    lufs: Mapped[float | None]
    is_duplicate_of: Mapped[int | None] = mapped_column(ForeignKey("tracks.id"))
    is_active: Mapped[int] = mapped_column(Integer, nullable=False, default=1)
    artwork_path: Mapped[str | None] = mapped_column(Text)
    created_at: Mapped[str] = mapped_column(Text, nullable=False)
    updated_at: Mapped[str] = mapped_column(Text, nullable=False)


class Feature(Base):
    __tablename__ = "features"

    track_id: Mapped[int] = mapped_column(ForeignKey("tracks.id", ondelete="CASCADE"), primary_key=True)
    embedding: Mapped[bytes | None] = mapped_column(LargeBinary)
    embedding_dim: Mapped[int | None]
    bpm: Mapped[float | None]
    key_name: Mapped[str | None] = mapped_column(Text)
    mode: Mapped[str | None] = mapped_column(Text)
    lufs: Mapped[float | None]
    cluster_id: Mapped[int | None]
    status: Mapped[str] = mapped_column(Text, nullable=False, default="pending")
    error: Mapped[str | None] = mapped_column(Text)
    computed_at: Mapped[str | None] = mapped_column(Text)


class Job(Base):
    __tablename__ = "jobs"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    kind: Mapped[str] = mapped_column(Text, nullable=False)
    status: Mapped[str] = mapped_column(Text, nullable=False, default="pending")
    payload_json: Mapped[str | None] = mapped_column(Text)
    result_json: Mapped[str | None] = mapped_column(Text)
    error: Mapped[str | None] = mapped_column(Text)
    created_at: Mapped[str] = mapped_column(Text, nullable=False)
    updated_at: Mapped[str] = mapped_column(Text, nullable=False)


class Playlist(Base):
    __tablename__ = "playlists"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    kind: Mapped[str] = mapped_column(Text, nullable=False)
    name: Mapped[str] = mapped_column(Text, nullable=False)
    created_at: Mapped[str] = mapped_column(Text, nullable=False)
    meta_json: Mapped[str | None] = mapped_column(Text)
    type: Mapped[str] = mapped_column(Text, nullable=False, default="generated")
    description: Mapped[str | None] = mapped_column(Text)
    updated_at: Mapped[str | None] = mapped_column(Text)
    cover_track_id: Mapped[int | None] = mapped_column(ForeignKey("tracks.id"))
    cover_artwork: Mapped[str | None] = mapped_column(Text)
    sort_mode: Mapped[str] = mapped_column(Text, nullable=False, default="manual")
    archived_at: Mapped[str | None] = mapped_column(Text)
    rule_schema_version: Mapped[int | None]
    rule_json: Mapped[str | None] = mapped_column(Text)
    allow_duplicates: Mapped[int] = mapped_column(Integer, nullable=False, default=0)
    owner_scope: Mapped[str] = mapped_column(Text, nullable=False, default="local")


class ListeningHistory(Base):
    __tablename__ = "listening_history"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    track_id: Mapped[int] = mapped_column(ForeignKey("tracks.id"), nullable=False)
    ts: Mapped[str] = mapped_column(Text, nullable=False)
    source: Mapped[str | None] = mapped_column(Text)
    action: Mapped[str] = mapped_column(Text, nullable=False)
    daypart: Mapped[str | None] = mapped_column(Text)
    weekday: Mapped[int | None]
    position_sec: Mapped[float | None]
    duration_sec: Mapped[float | None]
    listened_sec: Mapped[float | None]
    session_id: Mapped[str | None] = mapped_column(Text)
    reason: Mapped[str | None] = mapped_column(Text)


class Genre(Base):
    __tablename__ = "genres"

    id: Mapped[int] = mapped_column(Integer, primary_key=True, autoincrement=True)
    name: Mapped[str] = mapped_column(Text, unique=True, nullable=False)


class TrackGenre(Base):
    __tablename__ = "track_genres"
    __table_args__ = (UniqueConstraint("track_id", "genre_id", name="uq_track_genres_track_genre"),)

    track_id: Mapped[int] = mapped_column(ForeignKey("tracks.id", ondelete="CASCADE"), primary_key=True)
    genre_id: Mapped[int] = mapped_column(ForeignKey("genres.id", ondelete="CASCADE"), primary_key=True)


class ScanState(Base):
    __tablename__ = "scan_state"

    key: Mapped[str] = mapped_column(Text, primary_key=True)
    value: Mapped[str] = mapped_column(Text, nullable=False)


__all__ = ["Base", "Feature", "Genre", "Job", "ListeningHistory", "Playlist", "ScanState", "Track", "TrackGenre"]
