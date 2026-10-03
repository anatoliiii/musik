"""SQLAlchemy ORM sessions and the small legacy-query bridge.

Repositories are moved incrementally to mapped entities. The bridge lets older
repository query shapes run through SQLAlchemy's dialect and bind handling while
they are converted; it does not open a second SQLite connection.
"""
from __future__ import annotations

import re
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


def _qmark_template(sql: str) -> tuple[str, list[str]]:
    """Convert DB-API qmarks outside SQL strings/comments to named binds."""
    result: list[str] = []
    names: list[str] = []
    index = 0
    i = 0
    state = "normal"
    while i < len(sql):
        char = sql[i]
        nxt = sql[i + 1] if i + 1 < len(sql) else ""
        if state == "normal":
            if char == "'": state = "single"
            elif char == '"': state = "double"
            elif char == "`": state = "backtick"
            elif char == "[": state = "bracket"
            elif char == "-" and nxt == "-": state = "line_comment"
            elif char == "/" and nxt == "*": state = "block_comment"
            elif char == "?":
                name = f"musik_arg_{index}"
                result.append(f":{name}")
                names.append(name)
                index += 1
                i += 1
                continue
        elif state == "single" and char == "'":
            if nxt == "'":
                result.extend((char, nxt)); i += 2; continue
            state = "normal"
        elif state == "double" and char == '"':
            if nxt == '"':
                result.extend((char, nxt)); i += 2; continue
            state = "normal"
        elif state == "backtick" and char == "`":
            state = "normal"
        elif state == "bracket" and char == "]":
            state = "normal"
        elif state == "line_comment" and char == "\n":
            state = "normal"
        elif state == "block_comment" and char == "*" and nxt == "/":
            result.extend((char, nxt)); i += 2; state = "normal"; continue
        result.append(char)
        i += 1
    return "".join(result), names


def _bind_qmark(
    sql: str,
    parameters: Sequence[Any] | Mapping[str, Any] | None,
    profile_id: str | None = None,
) -> tuple[str, dict[str, Any]]:
    if parameters is None:
        bound: dict[str, Any] = {}
        if ":musik_profile" in sql:
            bound["musik_profile"] = profile_id or ""
        return sql, bound
    if isinstance(parameters, Mapping):
        bound = dict(parameters)
        if ":musik_profile" in sql:
            bound["musik_profile"] = profile_id or ""
        return sql, bound
    template, names = _qmark_template(sql)
    values = list(parameters)
    if len(names) != len(values):
        raise ValueError(f"query has {len(names)} placeholders but {len(values)} values")
    bound = dict(zip(names, values, strict=True))
    if ":musik_profile" in template:
        bound["musik_profile"] = profile_id or ""
    return template, bound


def _adapt_sql(sql: str, dialect_name: str) -> str:
    """Keep backend syntax at this adapter boundary while repositories migrate."""
    if dialect_name == "postgresql":
        ignore_conflict = bool(re.search(r"\bINSERT\s+OR\s+IGNORE\s+INTO\b", sql, re.I))
        if ignore_conflict:
            sql = re.sub(r"\bINSERT\s+OR\s+IGNORE\s+INTO\b", "INSERT INTO", sql, flags=re.I)
        if ignore_conflict and not re.search(r"\bON\s+CONFLICT\b", sql, re.I):
            sql = sql.rstrip().rstrip(";") + " ON CONFLICT DO NOTHING"
        sql = re.sub(r"datetime\(\s*'now'\s*\)", "CURRENT_TIMESTAMP", sql, flags=re.I)
        sql = re.sub(r"datetime\(\s*'now'\s*,\s*(:[A-Za-z_][A-Za-z0-9_]*)\s*\)", r"(CURRENT_TIMESTAMP + CAST(\1 AS INTERVAL))", sql, flags=re.I)
        sql = re.sub(r"\bdatetime\(\s*([^(),]+)\s*\)", r"CAST(\1 AS TIMESTAMPTZ)", sql, flags=re.I)
        sql = re.sub(r"CAST\(strftime\('%H',\s*([^)]*)\)\s+AS\s+INTEGER\)", r"CAST(EXTRACT(HOUR FROM CAST(\1 AS TIMESTAMPTZ)) AS INTEGER)", sql, flags=re.I)
        sql = re.sub(r"\bdate\(\s*([^()]+)\s*\)", r"CAST(CAST(\1 AS TIMESTAMPTZ) AS DATE)", sql, flags=re.I)
    return sql


_RETURNING_ID = re.compile(r"^\s*INSERT\s+INTO\s+(tracks|listening_history|jobs|playlists)\b", re.I)


class QueryResult:
    def __init__(self, result: CursorResult[Any], lastrowid: int | None = None):
        self._result = result
        self.rowcount = result.rowcount
        self.lastrowid = lastrowid

    def fetchone(self) -> MappingRow | None:
        row = self._result.fetchone()
        return MappingRow(row) if row is not None else None

    def fetchall(self) -> list[MappingRow]:
        return [MappingRow(row) for row in self._result.fetchall()]

    def __iter__(self):
        """Keep the small result bridge compatible with sqlite3 cursors."""
        return iter(self.fetchall())

    def close(self) -> None:
        self._result.close()


class ORMConnection:
    """One SQLAlchemy session exposed to repositories during one transaction."""

    def __init__(self, session: Session, profile_id: str | None = None):
        self.session = session
        self.profile_id = profile_id

    def execute(self, statement: Any, parameters: Sequence[Any] | Mapping[str, Any] | None = None) -> QueryResult:
        if not isinstance(statement, str):
            return QueryResult(self.session.execute(statement, parameters or {}))
        sql, bindings = _bind_qmark(statement, parameters, self.profile_id)
        sql = _adapt_sql(sql, self.dialect_name)
        returning = False
        if self.session.bind is not None and self.session.bind.dialect.name == "postgresql" and not re.search(r"\bRETURNING\b", sql, re.I):
            if _RETURNING_ID.match(sql):
                sql += " RETURNING id"
                returning = True
        result = self.session.execute(text(sql), bindings)
        lastrowid = None
        if returning:
            row = result.fetchone()
            lastrowid = int(row[0]) if row is not None else None
        elif self.dialect_name == "sqlite" and not result.returns_rows:
            lastrowid = getattr(result, "lastrowid", None)
        return QueryResult(result, lastrowid)

    def executemany(self, statement: str, parameters: Sequence[Sequence[Any]]) -> QueryResult:
        sql, _ = _qmark_template(statement)
        sql = _adapt_sql(sql, self.dialect_name)
        bound = [_bind_qmark(statement, values, self.profile_id)[1] for values in parameters]
        return QueryResult(self.session.execute(text(sql), bound))

    def commit(self) -> None:
        self.session.commit()

    def rollback(self) -> None:
        self.session.rollback()

    def close(self) -> None:
        self.session.close()

    @property
    def dialect_name(self) -> str:
        assert self.session.bind is not None
        return self.session.bind.dialect.name


__all__ = ["MappingRow", "ORMConnection", "engine_for", "session_for"]
