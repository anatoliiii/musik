"""Build migration metadata from the maintained legacy SQLite schema history.

The legacy v1-v5 SQLite migrations remain the adoption source for existing
installations. PostgreSQL DDL is compiled from their reflected SQLAlchemy
metadata so both dialects get the same tables, keys, constraints and indexes.
"""
from __future__ import annotations

import re
import sqlite3

from sqlalchemy import CheckConstraint, Double, LargeBinary, MetaData, create_engine, text


def reference_metadata(*, postgresql: bool = False) -> MetaData:
    from musik.db.migrations import migrate_connection

    raw = sqlite3.connect(":memory:")
    raw.execute("PRAGMA foreign_keys=ON")
    migrate_connection(raw)
    source_engine = create_engine("sqlite://", creator=lambda: raw)
    metadata = MetaData()
    metadata.reflect(bind=source_engine)
    source_engine.dispose()

    if not postgresql:
        return metadata

    json_valid = re.compile(r"json_valid\s*\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)", re.I)
    sqlite_default = re.compile(r"randomblob\s*\(|strftime\s*\(", re.I)
    for table in metadata.tables.values():
        for column in table.columns:
            if column.type.__class__.__name__.lower() == "blob":
                column.type = LargeBinary()
            elif column.type.__class__.__name__.lower() == "real":
                # SQLite REAL is an IEEE-754 double. PostgreSQL REAL is float32;
                # DOUBLE PRECISION keeps migrated numeric values lossless.
                column.type = Double()
            if column.server_default is not None and sqlite_default.search(str(column.server_default.arg)):
                column.server_default = None
        for constraint in tuple(table.constraints):
            if not isinstance(constraint, CheckConstraint):
                continue
            expression = str(constraint.sqltext)
            if "json_valid" in expression.lower():
                expression = json_valid.sub(
                    r"(\1 IS NOT NULL AND \1::jsonb IS NOT NULL)", expression
                )
                constraint.sqltext = text(expression)

    # SQLite v2-v5 enforce these trigger-only invariants. They are represented
    # as table constraints on PostgreSQL, where CHECK constraints cover INSERT
    # and UPDATE without relying on application-side validation alone.
    metadata.tables["listening_history"].append_constraint(CheckConstraint(
        "(metadata_json IS NULL AND metadata_schema_version IS NULL) OR "
        "(metadata_json IS NOT NULL AND metadata_schema_version IS NOT NULL "
        "AND metadata_schema_version > 0 AND metadata_json::jsonb IS NOT NULL)",
        name="ck_listening_history_versioned_metadata",
    ))
    metadata.tables["recommendation_impressions"].append_constraint(CheckConstraint(
        "(features_json IS NULL AND features_schema_version IS NULL) OR "
        "(features_json IS NOT NULL AND features_schema_version > 0 "
        "AND features_json::jsonb IS NOT NULL)",
        name="ck_impressions_versioned_features",
    ))
    metadata.tables["recommendation_impressions"].append_constraint(CheckConstraint(
        "impression_id IS NOT NULL AND queued_at IS NOT NULL AND source IS NOT NULL "
        "AND outcome IN ('pending','finished','partial','early_skip','superseded','abandoned','legacy') "
        "AND legacy IN (0,1)",
        name="ck_impressions_lifecycle",
    ))
    metadata.tables["playlists"].append_constraint(CheckConstraint(
        "(rule_json IS NULL AND rule_schema_version IS NULL) OR "
        "(rule_json IS NOT NULL AND rule_schema_version IS NOT NULL "
        "AND rule_schema_version > 0 AND rule_json::jsonb IS NOT NULL)",
        name="ck_playlists_versioned_rule",
    ))
    metadata.tables["taste_contexts"].append_constraint(CheckConstraint(
        "(seeds_json IS NULL AND seeds_schema_version IS NULL) OR "
        "(seeds_json IS NOT NULL AND seeds_schema_version IS NOT NULL "
        "AND seeds_schema_version > 0 AND seeds_json::jsonb IS NOT NULL)",
        name="ck_taste_contexts_versioned_seeds",
    ))
    metadata.tables["recommendation_requests"].append_constraint(CheckConstraint(
        "(policy_json IS NULL AND policy_schema_version IS NULL) OR "
        "(policy_json IS NOT NULL AND policy_schema_version IS NOT NULL "
        "AND policy_schema_version > 0 AND policy_json::jsonb IS NOT NULL)",
        name="ck_requests_versioned_policy",
    ))
    return metadata


def install_postgresql_append_only_guard(connection) -> None:
    connection.execute(text("""
        CREATE OR REPLACE FUNCTION musik_reject_listening_history_mutation()
        RETURNS trigger LANGUAGE plpgsql AS $$
        BEGIN
            RAISE EXCEPTION 'listening_history is append-only';
        END;
        $$
    """))
    connection.execute(text("""
        CREATE TRIGGER trg_listening_history_append_only
        BEFORE UPDATE OR DELETE ON listening_history
        FOR EACH ROW EXECUTE FUNCTION musik_reject_listening_history_mutation()
    """))
