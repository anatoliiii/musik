"""SQLAlchemy engines and sessions for the configured database.

Repositories use mapped entities and SQLAlchemy expressions, or portable text
SQL with named parameters through ORMConnection; one engine per database URL.
"""
from __future__ import annotations

from collections.abc import Mapping, Sequence
from pathlib import Path
from typing import Any
from urllib.parse import quote

from sqlalchemy import Engine, create_engine, event, text
from sqlalchemy.engine import CursorResult, Row
from sqlalchemy.orm import Session, sessionmaker
from musik.database_target import normalize_database_url as validate_database_url


def _sqlite_url(path: Path) -> str:
    absolute = path.expanduser().resolve()
    return "sqlite:///" + quote(str(absolute), safe="/")


def normalize_database_url(database_url: str | None, db_path: Path) -> str:
    if not database_url:
        return _sqlite_url(db_path)
    database_url = validate_database_url(database_url)
    if database_url.startswith("postgres://"):
        return "postgresql+psycopg://" + database_url[len("postgres://") :]
    if database_url.startswith("postgresql://"):
        return "postgresql+psycopg://" + database_url[len("postgresql://") :]
    if database_url.startswith("sqlite:///"):
        return database_url
    raise ValueError("unsupported MUSIK_DATABASE_URL scheme")


_ENGINES: dict[str, Engine] = {}


def engine_for(database_url: str | None, db_path: Path) -> Engine:
    url = normalize_database_url(database_url, db_path)
    engine = _ENGINES.get(url)
    if engine is not None:
        return engine
    sqlite = url.startswith("sqlite:")
    options: dict[str, Any] = {"pool_pre_ping": not sqlite}
    if sqlite:
        options["connect_args"] = {
            "check_same_thread": False, "timeout": 15, "isolation_level": "IMMEDIATE",
        }
    engine = create_engine(url, **options)
    if sqlite:
        @event.listens_for(engine, "connect")
        def _configure_sqlite(dbapi_connection: Any, _record: Any) -> None:
            cursor = dbapi_connection.cursor()
            cursor.execute("PRAGMA foreign_keys=ON")
            cursor.execute("PRAGMA journal_mode=WAL")
            cursor.execute("PRAGMA busy_timeout=15000")
            cursor.close()
    _ENGINES[url] = engine
    return engine


def session_for(database_url: str | None, db_path: Path) -> Session:
    factory = sessionmaker(bind=engine_for(database_url, db_path), expire_on_commit=False)
    return factory()


class MappingRow:
    """sqlite3.Row-compatible indexing over SQLAlchemy's portable row mapping."""

    def __init__(self, row: Row[Any]):
        self._row = row
        self._keys = tuple(row._mapping.keys())
        self._values = tuple(row)
        self._mapping = row._mapping

    def __getitem__(self, key: int | slice | str) -> Any:
        if isinstance(key, (int, slice)):
            return self._values[key]
        return self._mapping[key]

    def __iter__(self):
        return iter(self._values)

    def __len__(self) -> int:
        return len(self._values)

    def keys(self):
        return self._keys

    def get(self, key: str, default: Any = None) -> Any:
        return self._mapping.get(key, default)


class QueryResult:
    def __init__(self, result: CursorResult[Any]):
        self._result = result
        self.rowcount = result.rowcount

    def fetchone(self) -> MappingRow | None:
        row = self._result.fetchone()
        return MappingRow(row) if row is not None else None

    def fetchall(self) -> list[MappingRow]:
        return [MappingRow(row) for row in self._result.fetchall()]

    def scalar_one(self) -> Any:
        return self._result.scalar_one()

    def __iter__(self):
        """Keep the small result bridge compatible with sqlite3 cursors."""
        return iter(self.fetchall())

    def close(self) -> None:
        self._result.close()


class ORMConnection:
    """One SQLAlchemy session exposed to repositories during one transaction.

    Text SQL uses SQLAlchemy named parameters (``:name``) and is sent as
    written; it must use syntax both SQLite and PostgreSQL accept. Personal
    data queries pass ``profile_id`` explicitly as a bound value.
    """

    def __init__(self, session: Session, profile_id: str | None = None):
        self.session = session
        self.profile_id = profile_id

    def execute(self, statement: Any, parameters: Mapping[str, Any] | None = None) -> QueryResult:
        if isinstance(statement, str):
            statement = text(statement)
        return QueryResult(self.session.execute(statement, dict(parameters or {})))

    def executemany(self, statement: str, parameters: Sequence[Mapping[str, Any]]) -> QueryResult:
        return QueryResult(self.session.execute(text(statement), [dict(row) for row in parameters]))

    def commit(self) -> None:
        self.session.commit()

    def rollback(self) -> None:
        self.session.rollback()

    def close(self) -> None:
        self.session.close()

__all__ = ["MappingRow", "ORMConnection", "engine_for", "session_for"]
