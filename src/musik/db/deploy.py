"""One-shot Compose preparation: backup, migration, offline import and bootstrap."""
from __future__ import annotations

import fcntl
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
from datetime import datetime, timedelta, timezone
from urllib.parse import urlsplit
from uuid import uuid4

from sqlalchemy import inspect, text
from sqlalchemy.engine import make_url

from musik.database_target import normalize_database_url, resolve_sqlite_path
from musik.db.migrations import LATEST_SCHEMA_VERSION, migrate_database
from musik.db.orm import engine_for


def enabled(env, key):
    return env.get(key, "").lower() in {"1", "true", "yes"}


def _validate(env):
    if enabled(env, "MUSIK_MULTI_USER"):
        if env.get("MUSIK_PASSWORD") or env.get("MUSIK_API_TOKEN") or enabled(env, "MUSIK_AUTH_DISABLED"):
            raise ValueError("multi-user rejects shared password/token and disabled authentication")
        for key in ("MUSIK_PUBLIC_BASE_URL", "MUSIK_OIDC_ISSUER"):
            value = urlsplit(env.get(key, ""))
            if value.scheme != "https" or not value.hostname or value.username or value.query or value.fragment:
                raise ValueError(f"{key} requires HTTPS")
            if key == "MUSIK_PUBLIC_BASE_URL" and value.path not in {"", "/"}:
                raise ValueError("public base URL cannot contain a path")
        if not env.get("MUSIK_OIDC_CLIENT_ID"):
            raise ValueError("MUSIK_OIDC_CLIENT_ID is required")
        if env.get("MUSIK_CORS_ORIGINS") and env["MUSIK_CORS_ORIGINS"] != env["MUSIK_PUBLIC_BASE_URL"]:
            raise ValueError("multi-user requires same-origin CORS")
        if env.get("MUSIK_BOOTSTRAP_INVITE") and len(env["MUSIK_BOOTSTRAP_INVITE"]) < 32:
            raise ValueError("bootstrap secret must contain at least 32 random characters")


def _backup_sqlite(source: Path, directory: Path) -> Path:
    destination = directory / (datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid4().hex + ".sqlite")
    destination.touch(mode=0o600, exist_ok=False)
    try:
        with sqlite3.connect(source.resolve().as_uri() + "?mode=ro", uri=True) as original, sqlite3.connect(destination) as backup:
            original.backup(backup)
            if backup.execute("PRAGMA integrity_check").fetchone()[0] != "ok":
                raise ValueError("SQLite backup failed integrity verification")
            if backup.execute("PRAGMA foreign_key_check").fetchall():
                raise ValueError("SQLite backup contains foreign-key violations")
        return destination
    except Exception:
        destination.unlink(missing_ok=True)
        raise


def _backup_postgres(database_url: str, directory: Path) -> Path:
    from psycopg.conninfo import make_conninfo

    url = make_url(database_url)
    options = dict(url.query)
    options.update(host=url.host, dbname=url.database)
    query_password = options.pop("password", None)
    if url.username:
        options["user"] = url.username
    if url.port:
        options["port"] = str(url.port)
    # Credentials are passed in the child environment, never argv or logs.
    child_env = dict(os.environ)
    if url.password is not None or query_password is not None:
        child_env["PGPASSWORD"] = url.password if url.password is not None else query_password
    destination = directory / (datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid4().hex + ".dump")
    with destination.open("xb") as output:
        destination.chmod(0o600)
        result = subprocess.run(["pg_dump", "--format=custom", "--dbname", make_conninfo(**options)],
                                stdout=output, stderr=subprocess.PIPE, env=child_env)
    if result.returncode != 0:
        destination.unlink(missing_ok=True)
        raise ValueError("PostgreSQL backup failed; migration was not started")
    checked = subprocess.run(["pg_restore", "--list", str(destination)], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    if checked.returncode != 0:
        raise ValueError("PostgreSQL backup archive validation failed")
    return destination


def _bootstrap(engine, env):
    if not enabled(env, "MUSIK_MULTI_USER"):
        return
    now = datetime.now(timezone.utc)
    timestamp = now.strftime("%Y-%m-%dT%H:%M:%SZ")
    secret = env.get("MUSIK_BOOTSTRAP_INVITE", "")
    with engine.begin() as conn:
        owner = conn.scalar(text("SELECT value FROM installation_state WHERE key='legacy_user_id'"))
        suffix = " FOR UPDATE" if conn.dialect.name == "postgresql" else ""
        status = conn.scalar(text("SELECT status FROM users WHERE id=:id" + suffix), {"id": owner})
        linked = conn.scalar(text("SELECT count(*) FROM external_identities WHERE user_id=:id"), {"id": owner})
        admins = conn.scalar(text("SELECT count(*) FROM users u JOIN user_roles r ON r.user_id=u.id WHERE u.status='active' AND r.role='admin'"))
        if admins:
            return
        if status != "disabled" or linked:
            raise ValueError("installation owner already activated; bootstrap cannot recover administrator access")
        if not secret:
            pending = conn.scalar(text("SELECT count(*) FROM invitations WHERE target_user_id=:id AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > :now"), {"id": owner, "now": timestamp})
            if pending:
                return
            raise ValueError("set MUSIK_BOOTSTRAP_INVITE for the first administrator")
        hashed = hashlib.sha256(secret.encode()).hexdigest()
        existing = conn.execute(text("SELECT consumed_at,revoked_at,expires_at,issuer,target_user_id FROM invitations WHERE secret_hash=:hash"), {"hash": hashed}).first()
        if existing:
            if existing[0] or existing[1] or existing[2] <= timestamp or existing[3] != env["MUSIK_OIDC_ISSUER"] or existing[4] != owner:
                raise ValueError("set a new bootstrap secret; the previous invitation cannot be revived")
            return
        conn.execute(text("UPDATE invitations SET revoked_at=:now WHERE target_user_id=:id AND consumed_at IS NULL AND revoked_at IS NULL"), {"now": timestamp, "id": owner})
        conn.execute(text("INSERT INTO invitations(id,secret_hash,issuer,created_at,expires_at,target_user_id) VALUES (:id,:hash,:issuer,:created,:expires,:owner)"),
                     {"id": str(uuid4()), "hash": hashed, "issuer": env["MUSIK_OIDC_ISSUER"], "created": timestamp,
                      "expires": (now + timedelta(hours=24)).strftime("%Y-%m-%dT%H:%M:%SZ"), "owner": owner})


def prepare(env) -> None:
    _validate(env)
    data = Path(env.get("MUSIK_DATA_DIR", "/data"))
    path = Path(env.get("MUSIK_DB_PATH") or str(data / "db/musik.db"))
    url = env.get("MUSIK_DATABASE_URL") or None
    if url:
        url = normalize_database_url(url)
        if env.get("MUSIK_DB_PATH"):
            raise ValueError("use MUSIK_DATABASE_URL or MUSIK_DB_PATH, not both")
        if url.startswith("sqlite:"):
            path = resolve_sqlite_path(url, None, path)
    postgres = bool(url and url.startswith("postgresql:"))
    importing = enabled(env, "MUSIK_IMPORT_SQLITE")
    if importing and not postgres:
        raise ValueError("SQLite import requires a PostgreSQL destination")
    data.mkdir(parents=True, exist_ok=True)
    backups = data / "backups"
    backups.mkdir(mode=0o700, exist_ok=True)
    with (data / ".migration.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        existed = postgres or path.exists()
        if not postgres:
            path.parent.mkdir(parents=True, exist_ok=True)
        engine = engine_for(url, path)
        # A database-wide lock also coordinates migrations from other volumes.
        with engine.connect() as guard:
            if postgres:
                guard.execute(text("SELECT pg_advisory_lock(731840128)"))
                guard.commit()
            try:
                tables = set(inspect(guard).get_table_names()) if existed else set()
                revision = guard.scalar(text("SELECT version_num FROM alembic_version")) if "alembic_version" in tables else None
                pending = revision != f"musik_{LATEST_SCHEMA_VERSION}"
                source = Path(env.get("MUSIK_IMPORT_SQLITE_SOURCE", str(data / "db/musik.db"))).resolve()
                marker = guard.scalar(text("SELECT value FROM installation_state WHERE key='sqlite_import_completed'")) if "installation_state" in tables else None
                guard.commit()
                if postgres and not tables and source.is_file() and not importing:
                    raise ValueError("existing SQLite data detected; set MUSIK_IMPORT_SQLITE=1 before switching backend")
                if importing and marker and marker != str(source):
                    raise ValueError("a different SQLite source has already been imported")
                import_pending = importing and marker is None
                if ((pending and tables) or import_pending) and not enabled(env, "MUSIK_WRITERS_STOPPED"):
                    raise ValueError("existing data requires scripts/update-compose.sh to stop writers before migration/import")
                if pending and tables:
                    backup = _backup_postgres(url, backups) if postgres else _backup_sqlite(path, backups)
                    print(f"Verified pre-migration backup: {backup.name}", flush=True)
                if import_pending:
                    if not source.is_file():
                        raise ValueError("configured SQLite import source does not exist")
                    snapshot = _backup_sqlite(source, backups)
                    migrate_database(None, snapshot)
                    from musik.db.transfer import transfer_sqlite_to_postgres
                    report = transfer_sqlite_to_postgres(snapshot, url, completion_marker=str(source))
                    report_file = backups / (snapshot.stem + "-transfer.json")
                    with report_file.open("x") as output:
                        report_file.chmod(0o600)
                        json.dump(report, output, ensure_ascii=False, indent=2)
                    print(f"SQLite import verified: {report_file.name}", flush=True)
                elif pending:
                    migrate_database(url, path)
                _bootstrap(engine, env)
                print(f"Database ready: schema v{LATEST_SCHEMA_VERSION}, backend={'postgresql' if postgres else 'sqlite'}", flush=True)
            finally:
                if postgres:
                    guard.execute(text("SELECT pg_advisory_unlock(731840128)"))
                    guard.commit()


def main():
    try:
        prepare(os.environ)
    except ValueError as error:
        # Our validation messages contain no DSNs, credentials or raw secrets.
        print(f"Database preparation failed: {error}", flush=True)
        raise SystemExit(1) from None
    except Exception:
        # Driver exceptions can include DSNs; don't put them in container logs.
        print("Database preparation failed; services remain stopped. Check database connectivity, backup storage and configuration.", flush=True)
        raise SystemExit(1) from None


if __name__ == "__main__":
    main()
