"""Store password hashes for local accounts next to OIDC identities."""
from alembic import op
from sqlalchemy import Column, Text, inspect

from musik.db.migrations import migrate_connection


revision = "musik_14"
down_revision = "musik_13"
branch_labels = None
depends_on = None


def upgrade() -> None:
    connection = op.get_bind()
    if connection.dialect.name == "sqlite":
        migrate_connection(connection.connection.driver_connection)
        return
    columns = {column["name"] for column in inspect(connection).get_columns("external_identities")}
    if "password_hash" not in columns:
        op.add_column("external_identities", Column("password_hash", Text))


def downgrade() -> None:
    raise RuntimeError("musik data migrations are forward-only; restore a verified backup to roll back")
