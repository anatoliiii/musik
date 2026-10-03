from pathlib import Path

import pytest
from sqlalchemy import create_engine
from sqlalchemy.dialects import postgresql
from sqlalchemy.orm import Session
from sqlalchemy.schema import CreateIndex

from musik.database_target import normalize_database_url
from musik.db.orm import ORMConnection, _adapt_sql, _bind_qmark, _qmark_template


def test_qmark_adapter_skips_literals_and_comments() -> None:
    sql = "SELECT ?, '?' AS literal, \"?\" AS ident -- ?\n, ? /* ? */"
    template, names = _qmark_template(sql)
    assert names == ["musik_arg_0", "musik_arg_1"]
    assert template == "SELECT :musik_arg_0, '?' AS literal, \"?\" AS ident -- ?\n, :musik_arg_1 /* ? */"


def test_orm_connection_uses_one_session_for_legacy_queries(tmp_path: Path) -> None:
    engine = create_engine(f"sqlite:///{tmp_path / 'orm.sqlite'}")
    session = Session(engine)
    conn = ORMConnection(session)
    conn.execute("CREATE TABLE sample (id INTEGER PRIMARY KEY, value TEXT NOT NULL)")
    conn.executemany("INSERT INTO sample(value) VALUES (?)", [("one",), ("два",)])
    rows = conn.execute("SELECT id, value FROM sample ORDER BY id").fetchall()
    conn.commit()
    conn.close()
    engine.dispose()
    assert [(row[0], row["value"]) for row in rows] == [(1, "one"), (2, "два")]


def test_orm_connection_binds_server_selected_profile() -> None:
    sql, values = _bind_qmark(
        "SELECT id FROM sample WHERE owner_profile=:musik_profile AND id=?",
        (17,),
        "profile-a",
    )
    assert sql == "SELECT id FROM sample WHERE owner_profile=:musik_profile AND id=:musik_arg_0"
    assert values == {"musik_arg_0": 17, "musik_profile": "profile-a"}


def test_postgres_schema_keeps_profile_default_partial_unique_index() -> None:
    from musik.db.schema_reference import reference_metadata

    metadata = reference_metadata(postgresql=True)
    index = next(index for index in metadata.tables["profiles"].indexes if index.name == "idx_profiles_one_default")
    ddl = str(CreateIndex(index).compile(dialect=postgresql.dialect()))
    assert "WHERE" in ddl and "is_default = 1" in ddl and "deleted_at IS NULL" in ddl


def test_database_url_accepts_sqlite_and_postgres_without_logging_secrets() -> None:
    assert normalize_database_url("sqlite:////tmp/musik%20db.sqlite") == "sqlite:////tmp/musik%20db.sqlite"
    assert normalize_database_url("postgres://musik:secret@localhost:5432/musik") == "postgresql://musik:secret@localhost:5432/musik"
    with pytest.raises(ValueError, match="host and database"):
        normalize_database_url("postgresql:///musik")


def test_postgres_sql_adapter_only_translates_supported_legacy_forms() -> None:
    assert _adapt_sql("INSERT OR IGNORE INTO x(a) VALUES (:a)", "postgresql") == (
        "INSERT INTO x(a) VALUES (:a) ON CONFLICT DO NOTHING"
    )
    assert _adapt_sql("INSERT INTO x(a) VALUES (:a)", "postgresql") == "INSERT INTO x(a) VALUES (:a)"
    assert _adapt_sql("SELECT datetime('now')", "postgresql") == "SELECT CURRENT_TIMESTAMP"
    assert _adapt_sql("SELECT datetime(i.queued_at)", "postgresql") == "SELECT CAST(i.queued_at AS TIMESTAMPTZ)"
