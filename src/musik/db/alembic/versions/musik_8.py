"""Recognize the deployed home PostgreSQL v8 identity/profile contract.

This compatibility anchor is an Alembic revision, not upstream SQLite schema
version 8. Fresh databases arrive here after the current baseline; deployed
home databases start here and gain their missing catalog objects in v12.
"""
from alembic import op
from sqlalchemy import inspect

revision = "musik_8"
down_revision = "musik_5"
branch_labels = None
depends_on = None


def upgrade() -> None:
    connection = op.get_bind()
    inspector = inspect(connection)
    required = {"users", "profiles", "user_roles", "installation_state", "external_identities", "device_tokens"}
    missing = required - set(inspector.get_table_names())
    if missing:
        raise RuntimeError(f"home v8 identity contract is incomplete: {', '.join(sorted(missing))}")
    if connection.dialect.name == "sqlite":
        version = connection.exec_driver_sql("PRAGMA user_version").scalar_one()
        if version < 12:
            raise RuntimeError("legacy home SQLite needs an explicit history conversion before upgrade")


def downgrade() -> None:
    raise RuntimeError("musik data migrations are forward-only; restore a verified backup to roll back")
