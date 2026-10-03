"""Offline, transactional ownership migration for the existing SQLite schema."""
from __future__ import annotations

import re
import sqlite3
from datetime import datetime, timezone
from uuid import uuid4


PROFILE_TABLES = {
    "listening_history", "rec_stats", "recommendation_impressions", "transitions",
    "playlists", "playlist_tracks", "feature_weights", "user_profile_snapshots",
    "listen_later", "discover_tips", "favorites", "favorite_artists", "favorite_albums",
    "radio_shares", "play_sessions", "recommendation_requests", "taste_contexts",
    "taste_context_states", "session_contexts", "radio_rules", "event_contexts",
    "request_contexts", "track_stats", "taste_states", "taste_centroids",
    "transition_stats", "custom_tags", "track_tags", "track_preferences", "explore_arms",
    "radio_prefs",
    "model_versions", "training_runs",
}

# These are installation-wide identities, not per-profile business keys.
GLOBAL_KEYS = {
    "listening_history": "id", "recommendation_impressions": "id",
    "playlists": "id", "user_profile_snapshots": "id", "discover_tips": "id",
    "radio_shares": "token", "play_sessions": "id", "recommendation_requests": "request_id",
    "taste_contexts": "context_id", "radio_rules": "rule_id", "custom_tags": "tag_id",
    "training_runs": "run_id",
}

PARENT_REFS = {
    "playlist_tracks": [("playlist_id", "playlists", "id")],
    "taste_context_states": [("context_id", "taste_contexts", "context_id")],
    "session_contexts": [("context_id", "taste_contexts", "context_id")],
    "event_contexts": [("history_id", "listening_history", "id"), ("context_id", "taste_contexts", "context_id")],
    "request_contexts": [("request_id", "recommendation_requests", "request_id"), ("context_id", "taste_contexts", "context_id")],
    "radio_rules": [("context_id", "taste_contexts", "context_id")],
    "track_tags": [("tag_id", "custom_tags", "tag_id")],
    "listening_history": [("request_id", "recommendation_requests", "request_id")],
    "recommendation_impressions": [("request_id", "recommendation_requests", "request_id")],
    "training_runs": [("model_version", "model_versions", "model_version")],
}


def definitions(sql: str) -> list[str]:
    sql = re.sub(r"'(?:''|[^'])*'|--[^\n]*|/\*.*?\*/", lambda m: m[0] if m[0].startswith("'") else "", sql, flags=re.S)
    body = sql[sql.index("(") + 1:sql.rindex(")")]
    parts, depth, quote, start = [], 0, "", 0
    i = 0
    while i < len(body):
        ch = body[i]
        if quote:
            if ch == quote:
                if i + 1 < len(body) and body[i + 1] == quote:
                    i += 1
                else:
                    quote = ""
        elif ch in "'\"`":
            quote = ch
        elif ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
        elif ch == "," and depth == 0:
            parts.append(body[start:i].strip())
            start = i + 1
        i += 1
    parts.append(body[start:].strip())
    return parts


def profile_scope_unique_index(table: str, sql: str) -> str:
    match = re.search(rf"\bON\s+[\"`]?{re.escape(table)}[\"`]?(\s*)\(", sql, re.I)
    if not match:
        raise RuntimeError(f"cannot scope unique index for {table}: {sql}")
    opening = match.end() - 1
    first_column = sql[opening + 1 :].lstrip().split(",", 1)[0].strip()
    if re.match(r"[\"`]?profile_id\b", first_column, re.I):
        return sql
    return sql[: opening + 1] + "profile_id," + sql[opening + 1 :]


def migrate_profiles(conn: sqlite3.Connection) -> None:
    conn.commit()
    conn.execute("PRAGMA foreign_keys = OFF")
    try:
        conn.execute("BEGIN IMMEDIATE")
        conn.execute("ALTER TABLE invitations ADD COLUMN target_user_id TEXT REFERENCES users(id)")
        if any("profile_id" in {r[1] for r in conn.execute(f'PRAGMA table_info("{t}")')} for t in PROFILE_TABLES):
            raise RuntimeError("ambiguous existing profile ownership; restore backup before migration")
        now = datetime.now(timezone.utc).isoformat()
        user_id, profile_id = str(uuid4()), str(uuid4())
        conn.execute("INSERT INTO users VALUES (?, 'disabled', 'Installation owner', ?, ?)", (user_id, now, now))
        conn.execute("INSERT INTO user_roles VALUES (?, 'admin')", (user_id,))
        conn.execute("INSERT INTO user_roles VALUES (?, 'user')", (user_id,))
        conn.execute("INSERT INTO profiles(id,owner_user_id,name,is_default,created_at,updated_at) VALUES (?,?,'Main',1,?,?)", (profile_id, user_id, now, now))
        conn.execute("CREATE TABLE installation_state (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
        conn.executemany("INSERT INTO installation_state VALUES (?,?)", [("legacy_user_id", user_id), ("legacy_profile_id", profile_id)])
        metadata = {t: conn.execute("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", (t,)).fetchone()[0] for t in PROFILE_TABLES}
        objects = {t: conn.execute("SELECT type, sql FROM sqlite_master WHERE tbl_name=? AND type IN ('index','trigger') AND sql IS NOT NULL", (t,)).fetchall() for t in PROFILE_TABLES}
        counts = {t: conn.execute(f'SELECT count(*) FROM "{t}"').fetchone()[0] for t in PROFILE_TABLES}
        for table in sorted(PROFILE_TABLES):
            parts = definitions(metadata[table])
            constraints = []
            key_cols = [r[1] for r in sorted(conn.execute(f'PRAGMA table_info("{table}")'), key=lambda r:r[5]) if r[5]]
            rewritten = []
            for part in parts:
                if table not in GLOBAL_KEYS:
                    if re.match(r"PRIMARY\s+KEY\s*\(", part, re.I):
                        continue
                    part = re.sub(r"\bPRIMARY\s+KEY\b", "", part, flags=re.I)
                match = re.search(r"REFERENCES\s+(\w+)\s*\((\w+)\)(\s+ON\s+DELETE\s+(?:CASCADE|SET\s+NULL|RESTRICT))?", part, re.I)
                if match and match[1] in PROFILE_TABLES:
                    column = part.split()[0]
                    action = match[3] or ""
                    constraints.append(f"FOREIGN KEY(profile_id,{column}) REFERENCES {match[1]}(profile_id,{match[2]}){action}")
                    part = part[:match.start()] + part[match.end():]
                rewritten.append(part)
            rewritten.insert(0, f"profile_id TEXT NOT NULL DEFAULT '{profile_id}' REFERENCES profiles(id)")
            for column, parent, key in PARENT_REFS.get(table, []):
                if not any(f"FOREIGN KEY(profile_id,{column})" in c for c in constraints):
                    constraints.append(f"FOREIGN KEY(profile_id,{column}) REFERENCES {parent}(profile_id,{key})")
            if table in GLOBAL_KEYS:
                rewritten.append(f"UNIQUE(profile_id,{GLOBAL_KEYS[table]})")
            elif key_cols:
                rewritten.append("PRIMARY KEY(profile_id," + ",".join(key_cols) + ")")
                rewritten.append("CHECK(" + " AND ".join(c+" IS NOT NULL" for c in key_cols) + ")")
            rewritten.extend(constraints)
            temp = "__profile_" + table
            conn.execute(f'CREATE TABLE "{temp}" (' + ",\n".join(rewritten) + ")")
            columns = ",".join('"'+r[1]+'"' for r in conn.execute(f'PRAGMA table_info("{table}")'))
            conn.execute(f'INSERT INTO "{temp}" ({columns},profile_id) SELECT {columns},? FROM "{table}"', (profile_id,))
            conn.execute(f'DROP TABLE "{table}"')
            conn.execute(f'ALTER TABLE "{temp}" RENAME TO "{table}"')
            for kind, sql in objects[table]:
                if kind == "index" and "UNIQUE" in sql.upper():
                    sql = profile_scope_unique_index(table, sql)
                conn.execute(sql)
            conn.execute(f'CREATE INDEX "idx_{table}_profile" ON "{table}"(profile_id)')
            if conn.execute(f'SELECT count(*) FROM "{table}"').fetchone()[0] != counts[table]:
                raise RuntimeError(f"ownership migration lost rows in {table}")
        violations = conn.execute("PRAGMA foreign_key_check").fetchall()
        if violations:
            raise RuntimeError(f"ownership migration found {len(violations)} broken references")
        conn.execute("PRAGMA user_version = 7")
        conn.commit()
    except Exception:
        conn.rollback()
        raise
    finally:
        conn.execute("PRAGMA foreign_keys = ON")
