"""Identity and profile ownership: the boundary between the shared catalog and
personal data, made explicit for both engines.

The baseline adopts the SQLite history (steps 11 and 12 add users, profiles
and profile_id on personal tables) or creates the same schema on PostgreSQL
from its reflected metadata; this revision checks the result and, on a new
PostgreSQL database, seeds the installation owner and its main profile.
"""
from datetime import datetime, timezone
from uuid import uuid4

from alembic import op
from sqlalchemy import Column, Double, ForeignKey, Integer, PrimaryKeyConstraint, Text, inspect, text


revision = "musik_12"
down_revision = "musik_8"
branch_labels = None
depends_on = None


def upgrade() -> None:
    connection = op.get_bind()
    missing = {"users", "profiles", "external_identities", "auth_sessions", "invitations"} - set(
        inspect(connection).get_table_names()
    )
    if missing:
        raise RuntimeError(f"identity schema is incomplete: {', '.join(sorted(missing))}")
    if connection.dialect.name == "sqlite":
        version = connection.exec_driver_sql("PRAGMA user_version").scalar_one()
        if version < 12:
            raise RuntimeError("SQLite identity migration did not reach schema version 12")
    tables = set(inspect(connection).get_table_names())
    required = {"installation_state", "listening_history", "playlists", "recommendation_impressions"}
    missing = required - tables
    if missing:
        raise RuntimeError(f"profile ownership schema is incomplete: {', '.join(sorted(missing))}")
    for table in ("listening_history", "playlists", "recommendation_impressions"):
        columns = {column["name"] for column in inspect(connection).get_columns(table)}
        if "profile_id" not in columns:
            raise RuntimeError(f"profile ownership is missing {table}.profile_id")
    if connection.dialect.name == "postgresql":
        _upgrade_legacy_catalog(connection)
        seeded = connection.execute(
            text("SELECT 1 FROM installation_state WHERE key='legacy_profile_id'")
        ).first()
        if seeded is None:
            now = datetime.now(timezone.utc).isoformat()
            user_id, profile_id = str(uuid4()), str(uuid4())
            connection.execute(
                text("INSERT INTO users(id,status,display_name,created_at,updated_at) VALUES (:id,'disabled','Installation owner',:now,:now)"),
                {"id": user_id, "now": now},
            )
            connection.execute(text("INSERT INTO user_roles(user_id,role) VALUES (:id,'admin'),(:id,'user')"), {"id": user_id})
            connection.execute(
                text("INSERT INTO profiles(id,owner_user_id,name,is_default,created_at,updated_at) VALUES (:profile,:owner,'Main',1,:now,:now)"),
                {"profile": profile_id, "owner": user_id, "now": now},
            )
            connection.execute(text("INSERT INTO installation_state(key,value) VALUES ('legacy_user_id',:user),('legacy_profile_id',:profile)"), {"user": user_id, "profile": profile_id})


def _upgrade_legacy_catalog(connection) -> None:
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
            Column("position_sec", Double, nullable=False, server_default=text("0")),
            Column("listened_sec", Double, nullable=False, server_default=text("0")),
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

    columns = {column["name"] for column in inspect(connection).get_columns("tracks")}
    if "album_artist" not in columns:
        op.add_column("tracks", Column("album_artist", Text))
    if "is_compilation" not in columns:
        op.add_column("tracks", Column("is_compilation", Integer, nullable=False, server_default=text("0")))


def downgrade() -> None:
    raise RuntimeError("musik data migrations are forward-only; restore a verified backup to roll back")
