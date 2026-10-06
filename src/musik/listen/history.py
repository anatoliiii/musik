"""Listening history + transitions (step 5 / realtime contract)."""

from __future__ import annotations

from datetime import datetime, timezone
from typing import Any

from musik.db.schema import connect, utcnow

ACTIONS = frozenset(
    {"start", "finish", "skip", "like", "dislike", "progress", "track_end"}
)


def _daypart(hour: int) -> str:
    if 5 <= hour < 12:
        return "morning"
    if 12 <= hour < 17:
        return "afternoon"
    if 17 <= hour < 23:
        return "evening"
    return "night"


def record_listen(
    track_id: int,
    action: str,
    *,
    source: str = "cli",
    position_sec: float | None = None,
    duration_sec: float | None = None,
    listened_sec: float | None = None,
    session_id: str | None = None,
    reason: str | None = None,
    prev_track_id: int | None = None,
    transition_weight: float | None = None,
) -> int:
    """
    action: start|finish|skip|like|dislike|progress|track_end
    If prev_track_id given and action in (start, finish, track_end), bump transition.
    """
    action = action.lower().strip()
    if action not in ACTIONS:
        raise ValueError(f"unknown action: {action}")

    now = datetime.now(timezone.utc)
    ts = now.isoformat()
    daypart = _daypart(now.hour)
    weekday = now.weekday()

    with connect() as conn:
        hid = int(conn.execute(
            """
            INSERT INTO listening_history(
                track_id, ts, source, action, daypart, weekday,
                position_sec, duration_sec, listened_sec, session_id, reason, profile_id
            ) VALUES (
                :track_id, :ts, :source, :action, :daypart, :weekday,
                :position_sec, :duration_sec, :listened_sec, :session_id, :reason, :profile
            )
            RETURNING id
            """,
            {
                "track_id": track_id, "ts": ts, "source": source, "action": action,
                "daypart": daypart, "weekday": weekday, "position_sec": position_sec,
                "duration_sec": duration_sec, "listened_sec": listened_sec,
                "session_id": session_id, "reason": reason, "profile": conn.profile_id,
            },
        ).scalar_one())

        if prev_track_id is not None and prev_track_id != track_id and action in {
            "start",
            "finish",
            "track_end",
        }:
            w = float(transition_weight) if transition_weight is not None else 1.0
            conn.execute(
                """
                INSERT INTO transitions(from_id, to_id, weight, updated_at, profile_id)
                VALUES (:from_id, :to_id, :weight, :now, :profile)
                ON CONFLICT(profile_id, from_id, to_id) DO UPDATE SET
                    weight = transitions.weight + excluded.weight,
                    updated_at = excluded.updated_at
                """,
                {"from_id": prev_track_id, "to_id": track_id, "weight": w,
                 "now": utcnow(), "profile": conn.profile_id},
            )
        return hid


def bump_rec_stats(
    track_id: int,
    *,
    shown: int = 0,
    skipped_early: int = 0,
    completed: int = 0,
) -> None:
    now = utcnow()
    with connect() as conn:
        conn.execute(
            """
            INSERT INTO rec_stats(track_id, shown, skipped_early, completed, updated_at, profile_id)
            VALUES (:track_id, :shown, :skipped_early, :completed, :now, :profile)
            ON CONFLICT(profile_id, track_id) DO UPDATE SET
                shown = rec_stats.shown + excluded.shown,
                skipped_early = rec_stats.skipped_early + excluded.skipped_early,
                completed = rec_stats.completed + excluded.completed,
                updated_at = excluded.updated_at
            """,
            {"track_id": track_id, "shown": shown, "skipped_early": skipped_early,
             "completed": completed, "now": now, "profile": conn.profile_id},
        )


def recent_history(limit: int = 50) -> list[dict[str, Any]]:
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT h.id, h.track_id, h.ts, h.action, h.daypart, h.weekday,
                   h.listened_sec, h.duration_sec, h.reason, h.session_id,
                   t.artist, t.title
            FROM listening_history h
            JOIN tracks t ON t.id = h.track_id
            WHERE h.profile_id = :profile
            ORDER BY h.id DESC
            LIMIT :limit
            """,
            {"profile": conn.profile_id, "limit": limit},
        ).fetchall()
        return [dict(r) for r in rows]


def history_counts() -> dict[str, int]:
    with connect() as conn:
        rows = conn.execute(
            "SELECT action, COUNT(*) AS n FROM listening_history WHERE profile_id = :profile GROUP BY action",
            {"profile": conn.profile_id},
        ).fetchall()
        out = {r["action"]: int(r["n"]) for r in rows}
        out["total"] = sum(out.values())
        return out


def top_transitions(limit: int = 20) -> list[dict[str, Any]]:
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT tr.from_id, tr.to_id, tr.weight,
                   a.artist AS from_artist, a.title AS from_title,
                   b.artist AS to_artist, b.title AS to_title
            FROM transitions tr
            JOIN tracks a ON a.id = tr.from_id
            JOIN tracks b ON b.id = tr.to_id
            WHERE tr.profile_id = :profile
            ORDER BY tr.weight DESC
            LIMIT :limit
            """,
            {"profile": conn.profile_id, "limit": limit},
        ).fetchall()
        return [dict(r) for r in rows]
