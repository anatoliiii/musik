"""Mark the identity/profile schema generation in the shared Alembic chain.

The v5 baseline is adopted through the SQLite forward migrations or created
from their reflected metadata on PostgreSQL, so the base already contains the
identity tables. This revision makes that boundary explicit for both engines.
"""
from alembic import op
from sqlalchemy import inspect, text


revision = "musik_6"
down_revision = "musik_5"
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
        if version < 6:
            raise RuntimeError("SQLite identity migration did not reach schema version 6")


def downgrade() -> None:
    raise RuntimeError("musik data migrations are forward-only; restore a verified backup to roll back")
