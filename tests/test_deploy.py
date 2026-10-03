from pathlib import Path
import sqlite3

import pytest
from sqlalchemy import text

from musik.db.deploy import prepare
from musik.db.migrations import migrate_database
from musik.db.orm import engine_for


def environment(tmp_path: Path, **extra) -> dict[str, str]:
    return {"MUSIK_DATA_DIR": str(tmp_path), "MUSIK_DATABASE_URL": "sqlite:///" + str(tmp_path / "db.sqlite"), **extra}


def test_upgrade_requires_stopped_writers_and_backs_up_old_schema(tmp_path):
    path = tmp_path / "db.sqlite"
    migrate_database(None, path)
    with sqlite3.connect(path) as conn:
        conn.execute("DROP TABLE device_tokens")
        conn.execute("DELETE FROM installation_state WHERE key='active_admin_guard'")
        conn.execute("UPDATE alembic_version SET version_num='musik_7'")
        conn.execute("PRAGMA user_version=7")
    with pytest.raises(ValueError, match="update-compose"):
        prepare(environment(tmp_path))
    prepare(environment(tmp_path, MUSIK_WRITERS_STOPPED="1"))
    backups = list((tmp_path / "backups").glob("*.sqlite"))
    assert len(backups) == 1
    with sqlite3.connect(backups[0]) as conn:
        assert conn.execute("PRAGMA user_version").fetchone()[0] == 7
        assert conn.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
    with sqlite3.connect(path) as conn:
        assert conn.execute("PRAGMA user_version").fetchone()[0] == 8
    prepare(environment(tmp_path))
    assert len(list((tmp_path / "backups").glob("*.sqlite"))) == 1


def test_bootstrap_is_hashed_idempotent_and_never_revives_revoked_secret(tmp_path):
    env = environment(tmp_path, MUSIK_MULTI_USER="1", MUSIK_PUBLIC_BASE_URL="https://music.example.com",
                      MUSIK_OIDC_ISSUER="https://id.example.com", MUSIK_OIDC_CLIENT_ID="musik",
                      MUSIK_BOOTSTRAP_INVITE="a" * 64)
    prepare(env)
    prepare(env)
    engine = engine_for(env["MUSIK_DATABASE_URL"], tmp_path / "db.sqlite")
    with engine.begin() as conn:
        invitations = conn.execute(text("SELECT secret_hash,target_user_id FROM invitations")).all()
        assert len(invitations) == 1
        assert invitations[0][0] != env["MUSIK_BOOTSTRAP_INVITE"]
        conn.execute(text("UPDATE invitations SET revoked_at='2026-01-01T00:00:00Z'"))
    with pytest.raises(ValueError, match="new bootstrap secret"):
        prepare(env)
    with engine.connect() as conn:
        assert conn.scalar(text("SELECT count(*) FROM invitations WHERE revoked_at IS NULL")) == 0
    env["MUSIK_BOOTSTRAP_INVITE"] = "b" * 64
    prepare(env)
    with engine.begin() as conn:
        owner = conn.scalar(text("SELECT value FROM installation_state WHERE key='legacy_user_id'"))
        conn.execute(text("UPDATE users SET status='active' WHERE id=:id"), {"id": owner})
        conn.execute(text("INSERT INTO external_identities(issuer,subject,user_id,created_at) VALUES ('https://id.example.com','sub',:id,'2026-01-01T00:00:00Z')"), {"id": owner})
        conn.execute(text("UPDATE invitations SET consumed_at='2026-01-01T00:00:00Z' WHERE revoked_at IS NULL"))
    env["MUSIK_BOOTSTRAP_INVITE"] = "c" * 64
    prepare(env)
    with engine.connect() as conn:
        assert conn.scalar(text("SELECT count(*) FROM invitations")) == 2


def test_invalid_multiuser_configuration_fails_before_creating_database(tmp_path):
    with pytest.raises(ValueError):
        prepare(environment(tmp_path, MUSIK_MULTI_USER="1", MUSIK_PASSWORD="legacy"))
    assert not (tmp_path / "db.sqlite").exists()


def test_import_cannot_be_enabled_for_sqlite(tmp_path):
    with pytest.raises(ValueError, match="PostgreSQL"):
        prepare(environment(tmp_path, MUSIK_IMPORT_SQLITE="1"))
    assert not (tmp_path / "db.sqlite").exists()
