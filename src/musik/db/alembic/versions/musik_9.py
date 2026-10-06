"""Add remaster/collaborator catalog columns, artist photos and per-profile playback state."""
from alembic import op
from sqlalchemy import Column, Float, ForeignKey, Integer, PrimaryKeyConstraint, Text, inspect, text

from musik.db.migrations import migrate_connection


revision = "musik_9"
down_revision = "musik_8"
branch_labels = None
depends_on = None


def upgrade() -> None:
    connection = op.get_bind()
    if connection.dialect.name == "sqlite":
        migrate_connection(connection.connection.driver_connection)
        return

    # Fresh PostgreSQL databases already get these objects from the reflected
    # SQLite chain in musik_5; existing ones are brought up to the same shape.
    inspector = inspect(connection)
    track_columns = {column["name"] for column in inspector.get_columns("tracks")}
    if "is_remaster" not in track_columns:
        op.add_column("tracks", Column("is_remaster", Integer, nullable=False, server_default=text("0")))
    if "artist_segments" not in track_columns:
        op.add_column("tracks", Column("artist_segments", Text))

    tables = set(inspector.get_table_names())
    if "playback_state" not in tables:
        op.create_table(
            "playback_state",
            Column("profile_id", Text, ForeignKey("profiles.id"), nullable=False),
            Column("owner_scope", Text, nullable=False, server_default=text("'local'")),
            Column("session_id", Text, nullable=False, server_default=text("''")),
            Column("track_id", Integer, nullable=False, server_default=text("0")),
            Column("position_sec", Float, nullable=False, server_default=text("0")),
            Column("listened_sec", Float, nullable=False, server_default=text("0")),
            Column("playing", Integer, nullable=False, server_default=text("0")),
            Column("client_id", Text, nullable=False, server_default=text("''")),
            Column("updated_at", Text, nullable=False),
            PrimaryKeyConstraint("profile_id", "owner_scope"),
        )
    if "artist_photos" not in tables:
        op.create_table(
            "artist_photos",
            Column("id", Integer, primary_key=True, autoincrement=True),
            Column("name_key", Text, nullable=False, unique=True),
            Column("artist", Text, nullable=False),
            Column("path", Text),
            Column("status", Text, nullable=False),
            Column("source", Text, nullable=False, server_default=text("''")),
            Column("checked_at", Text, nullable=False),
        )
    if "artwork_lookups" not in tables:
        op.create_table(
            "artwork_lookups",
            Column("album_key", Text, primary_key=True),
            Column("status", Text, nullable=False),
            Column("checked_at", Text, nullable=False),
        )


def downgrade() -> None:
    raise RuntimeError("musik data migrations are forward-only; restore a verified backup to roll back")
