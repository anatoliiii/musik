"""Add the album-level credit and compilation flag to tracks."""
from alembic import op
from sqlalchemy import Column, Integer, Text, inspect, text

from musik.db.migrations import migrate_connection


revision = "musik_10"
down_revision = "musik_9"
branch_labels = None
depends_on = None


def upgrade() -> None:
    connection = op.get_bind()
    if connection.dialect.name == "sqlite":
        migrate_connection(connection.connection.driver_connection)
        return
    columns = {column["name"] for column in inspect(connection).get_columns("tracks")}
    if "album_artist" not in columns:
        op.add_column("tracks", Column("album_artist", Text))
    if "is_compilation" not in columns:
        op.add_column("tracks", Column("is_compilation", Integer, nullable=False, server_default=text("0")))


def downgrade() -> None:
    raise RuntimeError("musik data migrations are forward-only; restore a verified backup to roll back")
