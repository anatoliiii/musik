from pathlib import Path

import pytest
from sqlalchemy import create_engine
from sqlalchemy.dialects import postgresql
from sqlalchemy.orm import Session
from sqlalchemy.schema import CreateIndex

from musik.database_target import normalize_database_url
from musik.db.orm import ORMConnection


def test_orm_connection_runs_named_text_sql_in_one_session(tmp_path: Path) -> None:
    engine = create_engine(f"sqlite:///{tmp_path / 'orm.sqlite'}")
    session = Session(engine)
    conn = ORMConnection(session)
    conn.execute("CREATE TABLE sample (id INTEGER PRIMARY KEY, value TEXT NOT NULL)")
    conn.executemany("INSERT INTO sample(value) VALUES (:value)", [{"value": "one"}, {"value": "два"}])
    rows = conn.execute("SELECT id, value FROM sample ORDER BY id").fetchall()
    conn.commit()
    conn.close()
    engine.dispose()
    assert [(row[0], row["value"]) for row in rows] == [(1, "one"), (2, "два")]


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
