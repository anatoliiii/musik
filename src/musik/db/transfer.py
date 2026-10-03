"""Offline, source-preserving SQLite to PostgreSQL transfer and verification."""
from __future__ import annotations

import base64
import hashlib
import json
import math
import sqlite3
import tempfile
from decimal import Decimal
from pathlib import Path
from typing import Any

from sqlalchemy import MetaData, create_engine, func, inspect, select, text

from musik.database_target import normalize_database_url
from musik.db.migrations import LATEST_SCHEMA_VERSION, migrate_database, schema_version
from musik.db.orm import engine_for
from musik.db.schema_reference import reference_metadata


def _canonical(value: Any) -> Any:
    if isinstance(value, (bytes, bytearray, memoryview)):
        return {"bytes_b64": base64.b64encode(bytes(value)).decode("ascii")}
    if isinstance(value, Decimal):
        return {"decimal": format(value.normalize(), "f")}
    if isinstance(value, float):
        if math.isnan(value):
            return {"float": "nan"}
        if math.isinf(value):
            return {"float": "inf" if value > 0 else "-inf"}
        return {"float": format(value, ".17g")}
    if hasattr(value, "isoformat"):
        return value.isoformat()
    return value


def _checksum(rows: list[dict[str, Any]]) -> str:
    digest = hashlib.sha256()
    for row in rows:
        encoded = json.dumps(
            {key: _canonical(value) for key, value in row.items()},
            ensure_ascii=False, sort_keys=True, separators=(",", ":"),
        ).encode("utf-8")
        digest.update(len(encoded).to_bytes(8, "big"))
        digest.update(encoded)
    return digest.hexdigest()


def _ordered(table):
    primary_key = list(table.primary_key.columns)
    return primary_key or list(table.columns)


def _read_rows(connection, table) -> list[dict[str, Any]]:
    statement = select(table).order_by(*_ordered(table))
    return [dict(row._mapping) for row in connection.execute(statement)]


def _check_empty_destination(connection, metadata: MetaData) -> None:
    inspector = inspect(connection)
    names = set(inspector.get_table_names())
    unexpected = names - set(metadata.tables) - {"alembic_version"}
    if unexpected:
        raise ValueError("PostgreSQL destination contains tables outside the musik schema")
    for table in metadata.sorted_tables:
        if table.name in names:
            has_data = connection.execute(select(table.c[0]).limit(1)).first()
            if has_data:
                raise ValueError(f"PostgreSQL destination table {table.name} is not empty")


def transfer_sqlite_to_postgres(source_path: Path, destination_url: str) -> dict[str, Any]:
    """Copy a consistent read-only SQLite snapshot; never mutate the source."""
    source_path = source_path.expanduser().resolve(strict=True)
    if not source_path.is_file():
        raise ValueError("SQLite source must be a database file")
    destination_url = normalize_database_url(destination_url)
    if not destination_url.startswith("postgresql://"):
        raise ValueError("transfer destination must be PostgreSQL")

    with tempfile.TemporaryDirectory(prefix="musik-transfer-") as temp_dir:
        snapshot_path = Path(temp_dir) / "source.sqlite"
        source_uri = source_path.as_uri() + "?mode=ro"
        source = sqlite3.connect(source_uri, uri=True)
        try:
            source.execute("PRAGMA query_only=ON")
            snapshot = sqlite3.connect(snapshot_path)
            try:
                source.backup(snapshot)
            finally:
                snapshot.close()
        finally:
            source.close()

        raw_source = sqlite3.connect(f"file:{snapshot_path}?mode=ro", uri=True)
        raw_source.row_factory = sqlite3.Row
        try:
            integrity = raw_source.execute("PRAGMA integrity_check").fetchone()[0]
            if integrity != "ok":
                raise ValueError(f"SQLite backup integrity check failed: {integrity}")
            fk_errors = raw_source.execute("PRAGMA foreign_key_check").fetchall()
            if fk_errors:
                raise ValueError(f"SQLite source has {len(fk_errors)} foreign-key violations")
            version = schema_version(raw_source)
            if version != LATEST_SCHEMA_VERSION:
                raise ValueError(f"SQLite source schema is {version}; expected {LATEST_SCHEMA_VERSION}")
            source_metadata = MetaData()
            target_metadata = reference_metadata(postgresql=True)
            source_engine = create_engine("sqlite://", creator=lambda: raw_source)
            try:
                with source_engine.connect() as source_connection:
                    source_metadata.reflect(bind=source_connection)
                    source_tables = set(source_metadata.tables) - {"alembic_version"}
                    if source_tables != set(target_metadata.tables):
                        missing = sorted(set(target_metadata.tables) - source_tables)
                        extra = sorted(source_tables - set(target_metadata.tables))
                        raise ValueError(f"SQLite schema mismatch (missing={missing}, extra={extra})")
                    rows_by_table = {
                        table.name: _read_rows(source_connection, table)
                        for table in source_metadata.sorted_tables
                        if table.name != "alembic_version"
                    }
            finally:
                source_engine.dispose()
        finally:
            try:
                raw_source.close()
            except sqlite3.ProgrammingError:
                pass

        migrate_database(destination_url, source_path)
        destination_engine = engine_for(destination_url, source_path)
        report: dict[str, Any] = {
            "schema_version": LATEST_SCHEMA_VERSION,
            "source": str(source_path),
            "destination_backend": "postgresql",
            "tables": {},
        }
        try:
            with destination_engine.begin() as destination:
                _check_empty_destination(destination, target_metadata)
                for table in target_metadata.sorted_tables:
                    source_rows = rows_by_table[table.name]
                    for offset in range(0, len(source_rows), 500):
                        batch = source_rows[offset : offset + 500]
                        if batch:
                            destination.execute(table.insert(), batch)

                for table in target_metadata.sorted_tables:
                    primary_key = list(table.primary_key.columns)
                    if len(primary_key) == 1 and primary_key[0].type.__class__.__name__.lower() in {
                        "integer", "biginteger", "smallinteger",
                    }:
                        sequence = destination.scalar(
                            text("SELECT pg_get_serial_sequence(:table_name, :column_name)"),
                            {"table_name": table.name, "column_name": primary_key[0].name},
                        )
                        if sequence:
                            maximum = destination.scalar(select(func.max(primary_key[0])))
                            destination.execute(
                                text("SELECT setval(:sequence_name, :next_value, :is_called)"),
                                {"sequence_name": sequence, "next_value": max(int(maximum or 0), 1), "is_called": maximum is not None},
                            )

                for table in target_metadata.sorted_tables:
                    source_rows = rows_by_table[table.name]
                    target_rows = _read_rows(destination, table)
                    source_hash = _checksum(source_rows)
                    target_hash = _checksum(target_rows)
                    if len(source_rows) != len(target_rows) or source_hash != target_hash:
                        raise RuntimeError(f"transfer verification failed for table {table.name}")
                    report["tables"][table.name] = {
                        "rows": len(source_rows), "sha256": source_hash,
                    }
        finally:
            destination_engine.dispose()
        return report
