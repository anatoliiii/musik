"""Adopt the legacy SQLite history or create the current PostgreSQL schema."""
from alembic import op
from sqlalchemy import inspect

from musik.db.migrations import migrate_connection
from musik.db.schema_reference import install_postgresql_append_only_guard, reference_metadata


revision = "musik_5"
down_revision = None
branch_labels = None
depends_on = None


def upgrade() -> None:
    connection = op.get_bind()
    if connection.dialect.name == "sqlite":
        raw_connection = connection.connection.driver_connection
        migrate_connection(raw_connection)
        return

    tables = set(inspect(connection).get_table_names()) - {"alembic_version"}
    if tables:
        raise RuntimeError(
            "PostgreSQL baseline requires an empty database; use `musik db transfer` "
            "for an existing SQLite installation"
        )
    metadata = reference_metadata(postgresql=True)
    metadata.create_all(connection)
    install_postgresql_append_only_guard(connection)


def downgrade() -> None:
    raise RuntimeError("musik data migrations are forward-only; restore a verified backup to roll back")
