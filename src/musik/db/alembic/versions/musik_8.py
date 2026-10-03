"""Add user-owned device tokens and the administrator invariant lock row."""
from alembic import op
from sqlalchemy import Column, ForeignKey, Index, String, inspect, text

from musik.db.migrations import migrate_connection


revision = "musik_8"
down_revision = "musik_7"
branch_labels = None
depends_on = None


def upgrade() -> None:
    connection = op.get_bind()
    if connection.dialect.name == "sqlite":
        migrate_connection(connection.connection.driver_connection)
        return

    tables = set(inspect(connection).get_table_names())
    if "device_tokens" not in tables:
        op.create_table(
            "device_tokens",
            Column("id", String(64), primary_key=True),
            Column("secret_hash", String(64), nullable=False, unique=True),
            Column("user_id", String(64), ForeignKey("users.id", ondelete="CASCADE"), nullable=False),
            Column("profile_id", String(64), ForeignKey("profiles.id"), nullable=False),
            Column("name", String(128), nullable=False),
            Column("created_at", String, nullable=False),
            Column("last_used_at", String),
            Column("expires_at", String, nullable=False),
            Column("revoked_at", String),
        )
    indexes = {index["name"] for index in inspect(connection).get_indexes("device_tokens")}
    if "idx_device_tokens_user" not in indexes:
        op.create_index("idx_device_tokens_user", "device_tokens", ["user_id", "revoked_at", "expires_at"])
    connection.execute(
        text("INSERT INTO installation_state(key,value) VALUES ('active_admin_guard','1') ON CONFLICT(key) DO NOTHING")
    )


def downgrade() -> None:
    raise RuntimeError("musik data migrations are forward-only; restore a verified backup to roll back")
