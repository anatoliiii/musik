from __future__ import annotations

import sqlite3

import pytest

from musik.db.migrations import LATEST_SCHEMA_VERSION, migrate_db
from musik.db.schema import SCHEMA


def _version(path) -> int:
    with sqlite3.connect(path) as conn:
        return int(conn.execute("PRAGMA user_version").fetchone()[0])


def test_fresh_migration_is_idempotent(tmp_path) -> None:
    path = tmp_path / "fresh.db"
    assert migrate_db(path) == LATEST_SCHEMA_VERSION
    with sqlite3.connect(path) as conn:
        before = conn.execute(
            "SELECT type, name, sql FROM sqlite_master ORDER BY type, name"
        ).fetchall()
    assert migrate_db(path) == LATEST_SCHEMA_VERSION
    with sqlite3.connect(path) as conn:
        after = conn.execute(
            "SELECT type, name, sql FROM sqlite_master ORDER BY type, name"
        ).fetchall()
    assert before == after
    assert _version(path) == LATEST_SCHEMA_VERSION
    with sqlite3.connect(path) as conn:
        columns = {row[1] for row in conn.execute("PRAGMA table_info(device_tokens)")}
        assert columns == {
            "id", "secret_hash", "user_id", "profile_id", "name", "created_at",
            "last_used_at", "expires_at", "revoked_at",
        }
        assert conn.execute(
            "SELECT value FROM installation_state WHERE key='active_admin_guard'"
        ).fetchone() == ("1",)


def test_legacy_database_reaches_same_schema_and_preserves_rows(tmp_path) -> None:
    old_path = tmp_path / "old.db"
    fresh_path = tmp_path / "fresh.db"
    with sqlite3.connect(old_path) as conn:
        conn.executescript(SCHEMA)
        conn.executescript(
            """
            INSERT INTO tracks(id, path, title, created_at, updated_at)
            VALUES (1, '/music/a.flac', 'A', 'now', 'now');
            INSERT INTO listening_history(track_id, ts, action, weekday)
            VALUES (1, 'now', 'finish', 0);
            """
        )

    migrate_db(old_path)
    migrate_db(fresh_path)
    with sqlite3.connect(old_path) as old, sqlite3.connect(fresh_path) as fresh:
        old_tables = {
            row[0] for row in old.execute("SELECT name FROM sqlite_master WHERE type='table'")
        }
        fresh_tables = {
            row[0] for row in fresh.execute("SELECT name FROM sqlite_master WHERE type='table'")
        }
        assert old_tables == fresh_tables
        assert old.execute("SELECT title FROM tracks WHERE id=1").fetchone()[0] == "A"
        # Legacy Python weekday 0 was Sunday; migration standardizes Monday=0.
        assert old.execute("SELECT weekday FROM listening_history").fetchone()[0] == 6
        assert _version(old_path) == _version(fresh_path) == LATEST_SCHEMA_VERSION


def test_foundation_schema_has_normalized_references_and_versioned_json(tmp_path) -> None:
    path = tmp_path / "foundation.db"
    migrate_db(path)
    with sqlite3.connect(path) as conn:
        tables = {
            row[0] for row in conn.execute("SELECT name FROM sqlite_master WHERE type='table'")
        }
        cols = {row[1] for row in conn.execute("PRAGMA table_info(playlists)")}
        assert {"type", "rule_json", "archived_at", "updated_at"} <= cols
        item_cols = {row[1] for row in conn.execute("PRAGMA table_info(playlist_tracks)")}
        assert {"item_id", "source", "added_at"} <= item_cols
        assert {
            "recommendation_requests",
            "request_contexts",
            "event_contexts",
            "taste_contexts",
            "taste_context_states",
            "session_contexts",
            "entity_vectors",
            "radio_rules",
            "model_versions",
            "training_runs",
            "explore_arms",
            "radio_prefs",
            "transition_stats",
            "custom_tags",
            "track_tags",
            "track_preferences",
        } <= tables

        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                """INSERT INTO taste_contexts(
                       context_id, kind, name, activation_schema_version, activation_json,
                       created_at, updated_at
                   ) VALUES ('bad', 'mood', 'Bad', NULL, '{}', 'now', 'now')"""
            )

        conn.execute(
            """INSERT INTO tracks(path, created_at, updated_at)
               VALUES ('/music/event.flac', 'now', 'now')"""
        )
        track_id = conn.execute("SELECT last_insert_rowid()").fetchone()[0]
        with pytest.raises(sqlite3.IntegrityError, match="versioned JSON"):
            conn.execute(
                """INSERT INTO listening_history(
                       track_id, ts, action, metadata_json
                   ) VALUES (?, 'now', 'start', '{}')""",
                (track_id,),
            )
        conn.execute(
            """INSERT INTO listening_history(
                   track_id, ts, action, event_id, event_schema_version
               ) VALUES (?, 'now', 'start', 'event-1', 1)""",
            (track_id,),
        )
        with pytest.raises(sqlite3.IntegrityError, match="append-only"):
            conn.execute(
                "UPDATE listening_history SET action='finish' WHERE event_id='event-1'"
            )


def test_identity_schema_enforces_profile_and_oidc_ownership(tmp_path) -> None:
    path = tmp_path / "identity.db"
    migrate_db(path)
    with sqlite3.connect(path) as conn:
        conn.execute("PRAGMA foreign_keys = ON")
        conn.execute(
            "INSERT INTO users(id, status, display_name, created_at, updated_at) "
            "VALUES ('user-a', 'active', 'A', 'now', 'now')"
        )
        conn.execute(
            "INSERT INTO users(id, status, display_name, created_at, updated_at) "
            "VALUES ('user-b', 'active', 'B', 'now', 'now')"
        )
        conn.execute(
            "INSERT INTO profiles(id, owner_user_id, name, is_default, created_at, updated_at) "
            "VALUES ('profile-a', 'user-a', 'Main', 1, 'now', 'now')"
        )
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO profiles(id, owner_user_id, name, is_default, created_at, updated_at) "
                "VALUES ('profile-a2', 'user-a', 'Other', 1, 'now', 'now')"
            )
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO profiles(id, owner_user_id, name, is_default, created_at, updated_at) "
                "VALUES ('orphan', 'missing', 'Orphan', 0, 'now', 'now')"
            )
        conn.execute(
            "INSERT INTO external_identities(issuer, subject, user_id, created_at) "
            "VALUES ('https://issuer.test', 'subject-1', 'user-a', 'now')"
        )
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO external_identities(issuer, subject, user_id, created_at) "
                "VALUES ('https://issuer.test', 'subject-1', 'user-b', 'now')"
            )
        conn.execute(
            "INSERT INTO external_identities(issuer, subject, user_id, created_at) "
            "VALUES ('https://other.test', 'subject-1', 'user-b', 'now')"
        )


def test_profile_migration_scopes_unique_event_keys_per_profile(tmp_path) -> None:
    path = tmp_path / "profile-unique.db"
    migrate_db(path)
    with sqlite3.connect(path) as conn:
        conn.execute("PRAGMA foreign_keys = ON")
        legacy_profile = conn.execute(
            "SELECT value FROM installation_state WHERE key='legacy_profile_id'"
        ).fetchone()[0]
        conn.execute(
            "INSERT INTO users(id,status,display_name,created_at,updated_at) "
            "VALUES ('user-b','active','B','now','now')"
        )
        conn.execute(
            "INSERT INTO profiles(id,owner_user_id,name,is_default,created_at,updated_at) "
            "VALUES ('profile-b','user-b','Main',1,'now','now')"
        )
        conn.execute("INSERT INTO tracks(path,created_at,updated_at) VALUES ('/a.flac','now','now')")
        track_id = conn.execute("SELECT last_insert_rowid()").fetchone()[0]
        for profile in (legacy_profile, "profile-b"):
            conn.execute(
                """INSERT INTO listening_history(
                       profile_id,track_id,ts,action,event_id,event_schema_version
                   ) VALUES (?,?,'now','track_start','same-client-event',1)""",
                (profile, track_id),
            )
        assert conn.execute(
            "SELECT count(*) FROM listening_history WHERE event_id='same-client-event'"
        ).fetchone()[0] == 2
