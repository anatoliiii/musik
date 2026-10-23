"""VK-style mix pack. Shelf kinds come from the player contract
(player/internal/playback/mixes.json); this module only knows how to build them.
"""

from __future__ import annotations

import json
import os
from datetime import date, datetime, timedelta, timezone
from pathlib import Path
from typing import Any

import numpy as np

from musik.brain.blocks import blocked_track_ids
from musik.brain.generators import (
    PlaylistBuild,
    _forbidden_rows,
    _noisy_query,
    _taste_vector,
    generate_daily,
    generate_weekly,
)
from musik.brain.ranker import rank_index
from musik.brain.store import latest_playlist
from musik.config import get_settings
from musik.db.schema import connect, utcnow
from musik.index.brute import EmbeddingIndex, load_index

_PLAIN_KINDS = frozenset({"for_you", "daily", "new_releases", "weekly"})


def mix_contract_path() -> Path:
    """The shelf registry owned by the Go player."""
    override = os.environ.get("MUSIK_MIX_CONTRACT", "").strip()
    if override:
        return Path(override)
    for parent in Path(__file__).resolve().parents:
        candidate = parent / "player" / "internal" / "playback" / "mixes.json"
        if candidate.is_file():
            return candidate
    raise FileNotFoundError("player mix contract mixes.json was not found")


def load_mix_contract() -> list[dict[str, Any]]:
    """Read the same shelf the player serves and sends in mix_pack jobs."""
    with mix_contract_path().open(encoding="utf-8") as fh:
        data = json.load(fh)
    if not isinstance(data, list) or not data:
        raise ValueError("mix contract must be a non-empty list")
    return data


def resolve_mix_contract(mixes: list[dict[str, Any]] | None) -> list[dict[str, Any]]:
    """A job payload wins. Otherwise the on-disk registry, generated shelves only."""
    if mixes is None:
        return [item for item in load_mix_contract() if item.get("source") == "generated"]
    if not isinstance(mixes, list):
        raise ValueError("mix contract must be a list")
    return mixes


def ensure_mix_generator(item: dict[str, Any]) -> None:
    """Reject a shelf the worker cannot build, instead of skipping it."""
    kind = str(item.get("kind") or "")
    if kind in _PLAIN_KINDS:
        return
    weekday = item.get("weekday")
    if kind.startswith("weekday_") and isinstance(weekday, int) and not isinstance(weekday, bool) and 0 <= weekday <= 6:
        return
    raise ValueError(f"worker has no generator for mix kind {kind!r}")


def _weekday_taste(index: EmbeddingIndex, weekday: int) -> tuple[np.ndarray, str, int]:
    """Blend global taste with tracks historically played on this weekday."""
    base, src = _taste_vector(index)
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT track_id, COUNT(*) AS c
            FROM listening_history
            WHERE profile_id = :profile
              AND weekday = :weekday
              AND action IN ('like', 'finish', 'track_end')
              AND (reason IS NULL OR reason != 'skipped')
            GROUP BY track_id
            ORDER BY c DESC
            LIMIT 40
            """,
            {"profile": conn.profile_id, "weekday": weekday},
        ).fetchall()
    if not rows:
        return base, src, 0
    acc = np.zeros(index.dim, dtype=np.float64)
    wsum = 0.0
    for r in rows:
        row = index.row_of(int(r["track_id"]))
        if row is None:
            continue
        w = float(r["c"])
        acc += w * index.matrix[row]
        wsum += w
    if wsum < 1e-9:
        return base, src, 0
    day_vec = (acc / wsum).astype(np.float32)
    n = float(np.linalg.norm(day_vec))
    if n > 1e-12:
        day_vec = day_vec / n
    blended = 0.55 * base + 0.45 * day_vec
    blended = blended / (np.linalg.norm(blended) + 1e-12)
    return blended.astype(np.float32), f"{src}+weekday_{weekday}", len(rows)


def _for_you_forbidden_rows(
    index: EmbeddingIndex,
    *,
    recent_days: int,
) -> set[int]:
    """Rows shown in the previous mix or heard recently; used as a soft exclusion."""
    track_ids: set[int] = set()
    previous = latest_playlist("for_you")
    if previous:
        track_ids.update(int(t["track_id"]) for t in previous["tracks"])
    # Stored instants are UTC ISO-8601 text; a whole-second cutoff without a
    # zone suffix orders correctly against both "Z" and "+00:00" forms.
    cutoff = (datetime.now(timezone.utc) - timedelta(days=max(1, recent_days))).strftime("%Y-%m-%dT%H:%M:%S")
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT DISTINCT track_id
            FROM listening_history
            WHERE profile_id = :profile
              AND ts >= :cutoff
              AND action IN ('start', 'finish', 'track_end', 'like', 'dislike')
            """,
            {"profile": conn.profile_id, "cutoff": cutoff},
        ).fetchall()
    track_ids.update(int(r["track_id"]) for r in rows)
    return {
        row
        for track_id in track_ids
        if (row := index.row_of(track_id)) is not None
    }


def generate_for_you(
    *,
    size: int = 40,
    explore_ratio: float | None = None,
    seed: int | None = None,
    recent_days: int = 7,
    forbidden_track_ids: set[int] | None = None,
) -> PlaylistBuild:
    """Strong personalization — 'Для вас'."""
    index = load_index()
    if index.size == 0:
        raise RuntimeError("Нет ready-эмбеддингов")
    settings = get_settings()
    explore_ratio = settings.explore_ratio if explore_ratio is None else explore_ratio
    if seed is None:
        seed = int(date.today().strftime("%Y%m%d"))
    rng = np.random.default_rng(seed)
    center, center_src = _taste_vector(index)
    query = _noisy_query(center, rng, 0.02)
    pack_forbidden = _forbidden_rows(index, forbidden_track_ids)
    soft_forbidden = (
        _for_you_forbidden_rows(index, recent_days=recent_days) | pack_forbidden
    )
    preferred_size = min(size, max(0, index.size - len(soft_forbidden)))
    ranked = rank_index(
        index,
        taste=query,
        forbidden=soft_forbidden,
        size=preferred_size,
        explore_ratio=explore_ratio,
    )
    if len(ranked) < min(size, index.size):
        used = {int(item["row"]) for item in ranked} | pack_forbidden
        ranked.extend(
            rank_index(
                index,
                taste=query,
                forbidden=used,
                size=min(size, index.size) - len(ranked),
                explore_ratio=explore_ratio,
            )
        )
    entries = [
        {
            "track_id": int(item["track_id"]),
            "explanation": f"для вас · cosine {float(item['sim_taste']):.3f}"
            + (
                " · explore"
                if item["source"] in {"explore_adjacent", "wildcard", "resurface", "new_in_library"}
                else ""
            ),
        }
        for item in ranked[:size]
    ]
    return PlaylistBuild(
        kind="for_you",
        name="Для вас",
        entries=entries,
        meta={
            "size": len(entries),
            "center": center_src,
            "seed": seed,
            "recent_days": recent_days,
            "excluded": len(soft_forbidden),
            "pack_excluded": len(pack_forbidden),
            "explore_ratio": explore_ratio,
            "method": "ranker+quotas+recent-exclusion",
        },
    )


def generate_weekday(
    weekday: int,
    *,
    kind: str,
    title: str,
    size: int = 30,
    explore_ratio: float | None = None,
    forbidden_track_ids: set[int] | None = None,
) -> PlaylistBuild:
    """Плейлист дня недели (0=Пн … 6=Вс). kind and title come from the mix contract."""
    if weekday < 0 or weekday > 6:
        raise ValueError("weekday 0..6")
    index = load_index()
    if index.size == 0:
        raise RuntimeError("Нет ready-эмбеддингов")
    settings = get_settings()
    explore_ratio = settings.explore_ratio if explore_ratio is None else explore_ratio
    key = kind
    center, center_src, signal_count = _weekday_taste(index, weekday)
    # Rotate nightly while preserving a deterministic result for the same day.
    seed = int(f"{date.today().strftime('%Y%m%d')}{weekday}")
    rng = np.random.default_rng(seed)
    # With no weekday history all days share the global profile. A stronger
    # seeded perturbation prevents those fallback shelves from collapsing into
    # the same ranking while remaining deterministic for the current night.
    noise_scale = 0.035 if signal_count == 0 else 0.015
    q = _noisy_query(center, rng, noise_scale)
    forbidden = _forbidden_rows(index, forbidden_track_ids)
    target_size = min(size, max(0, index.size - len(forbidden)))
    ranked = rank_index(
        index,
        taste=q,
        forbidden=forbidden,
        size=target_size,
        explore_ratio=explore_ratio,
    )
    if len(ranked) < min(size, index.size):
        used = {int(item["row"]) for item in ranked} | forbidden
        ranked.extend(
            rank_index(
                index,
                taste=q,
                forbidden=used,
                size=min(size, index.size) - len(ranked),
                explore_ratio=explore_ratio,
            )
        )
    entries = [
        {
            "track_id": int(item["track_id"]),
            "explanation": f"{title} · cosine {float(item['sim_taste']):.3f}"
            + (
                " · explore"
                if item["source"] in {"explore_adjacent", "wildcard", "resurface", "new_in_library"}
                else ""
            ),
        }
        for item in ranked[:size]
    ]
    return PlaylistBuild(
        kind=key,
        name=f"Микс · {title}",
        entries=entries,
        meta={
            "weekday": weekday,
            "title": title,
            "size": len(entries),
            "center": center_src,
            "seed": seed,
            "weekday_signals": signal_count,
            "noise_scale": noise_scale,
            "pack_excluded": len(forbidden),
            "explore_ratio": explore_ratio,
        },
    )


def generate_new_releases(
    *,
    size: int = 25,
    days: int = 14,
    forbidden_track_ids: set[int] | None = None,
) -> PlaylistBuild:
    """Новинки из недавно добавленных треков / discover tips."""
    index = load_index()
    if index.size == 0:
        raise RuntimeError("Нет ready-эмбеддингов")
    cutoff = datetime.now(timezone.utc) - timedelta(days=days)
    center, _ = _taste_vector(index)
    sims = index.sims_to_vector(center)
    forbidden_track_ids = forbidden_track_ids or set()

    with connect() as conn:
        tip_rows = conn.execute(
            """
            SELECT track_ids_json, score FROM discover_tips
            WHERE profile_id = :profile AND kind = 'new_album'
            ORDER BY score DESC LIMIT 30
            """,
            {"profile": conn.profile_id},
        ).fetchall()
        track_rows = conn.execute(
            """
            SELECT id, created_at FROM tracks
            WHERE is_active = 1 AND is_duplicate_of IS NULL
            """
        ).fetchall()

    cand_ids: list[int] = []
    for tr in tip_rows:
        try:
            ids = json.loads(tr["track_ids_json"] or "[]")
        except json.JSONDecodeError:
            ids = []
        for tid in ids:
            cand_ids.append(int(tid))

    created_ok: set[int] = set()
    for r in track_rows:
        try:
            ts = datetime.fromisoformat(str(r["created_at"]).replace("Z", "+00:00"))
        except ValueError:
            continue
        if ts >= cutoff:
            created_ok.add(int(r["id"]))

    # prefer tip tracks, then any recently scanned
    ordered_ids: list[int] = []
    seen: set[int] = set()
    for tid in cand_ids + sorted(created_ok, key=lambda t: -float(sims[index.row_of(t)]) if index.row_of(t) is not None else 0):
        if tid in seen or tid in forbidden_track_ids:
            continue
        if index.row_of(tid) is None:
            continue
        seen.add(tid)
        ordered_ids.append(tid)
        if len(ordered_ids) >= size:
            break

    # score by taste among candidates
    scored = []
    for tid in ordered_ids:
        row = index.row_of(tid)
        if row is None:
            continue
        scored.append((tid, float(sims[row])))
    scored.sort(key=lambda x: -x[1])

    entries = [
        {
            "track_id": tid,
            "explanation": f"новинка · cosine {sc:.3f} к вкусу",
        }
        for tid, sc in scored[:size]
    ]
    return PlaylistBuild(
        kind="new_releases",
        name="Новинки",
        entries=entries,
        meta={
            "size": len(entries),
            "days": days,
            "empty": len(entries) == 0,
            "pack_excluded": len(forbidden_track_ids),
        },
    )


def _mix_size(
    kind: str,
    *,
    daily_size: int,
    for_you_size: int,
    weekday_size: int,
    weekly_size: int,
    new_size: int,
) -> int:
    if kind == "for_you":
        return for_you_size
    if kind == "daily":
        return daily_size
    if kind == "new_releases":
        return new_size
    if kind == "weekly":
        return weekly_size
    return weekday_size


def _build_contract_mix(
    item: dict[str, Any],
    *,
    size: int,
    forbidden: set[int],
) -> PlaylistBuild:
    kind = str(item["kind"])
    if kind == "for_you":
        return generate_for_you(size=size, forbidden_track_ids=forbidden)
    if kind == "daily":
        build = generate_daily(size=size, forbidden_track_ids=forbidden)
        build.name = f"На сегодня · {date.today().strftime('%d.%m')}"
        return build
    if kind == "new_releases":
        return generate_new_releases(size=size, forbidden_track_ids=forbidden)
    if kind == "weekly":
        return generate_weekly(size=size, forbidden_track_ids=forbidden)
    return generate_weekday(
        int(item["weekday"]),
        kind=kind,
        title=str(item["title"]),
        size=size,
        forbidden_track_ids=forbidden,
    )


def generate_mix_pack(
    *,
    daily_size: int = 30,
    for_you_size: int = 40,
    weekday_size: int = 30,
    weekly_size: int = 50,
    new_size: int = 25,
    reserve_prefix: int = 10,
    mixes: list[dict[str, Any]] | None = None,
) -> dict[str, Any]:
    """Build and persist the shelves in the player mix contract."""
    contract = resolve_mix_contract(mixes)
    for item in contract:
        ensure_mix_generator(item)

    results: dict[str, Any] = {}
    reserved: set[int] = set(blocked_track_ids())
    weekdays: list[dict[str, Any]] = []

    def reserve(build: PlaylistBuild) -> None:
        reserved.update(
            int(entry["track_id"])
            for entry in build.entries[: max(1, reserve_prefix)]
        )

    for item in contract:
        kind = str(item["kind"])
        build = _build_contract_mix(
            item,
            size=_mix_size(
                kind,
                daily_size=daily_size,
                for_you_size=for_you_size,
                weekday_size=weekday_size,
                weekly_size=weekly_size,
                new_size=new_size,
            ),
            forbidden=reserved,
        )
        if kind == "new_releases":
            playlist_id = build.persist() if build.entries else None
            results["new_releases"] = {
                "id": playlist_id,
                "name": build.name,
                "n": len(build.entries),
                "empty": len(build.entries) == 0,
            }
        elif item.get("weekday") is not None:
            playlist_id = build.persist()
            weekdays.append({"kind": kind, "id": playlist_id, "name": build.name, "n": len(build.entries)})
        else:
            playlist_id = build.persist()
            results[kind] = {"id": playlist_id, "name": build.name, "n": len(build.entries)}
        reserve(build)

    results["weekdays"] = weekdays
    results["reserved_prefix"] = max(1, reserve_prefix)
    results["reserved_tracks"] = len(reserved)
    results["generated_at"] = utcnow()
    return results


def mix_catalog() -> list[dict[str, Any]]:
    """Shelf cards in the same order as the player registry."""
    today_wd = date.today().weekday()  # Mon=0
    shelves: list[dict[str, Any]] = []
    for item in load_mix_contract():
        kind = str(item["kind"])
        title = str(item["title"])
        subtitle = str(item.get("subtitle") or "")
        source = str(item.get("source") or "generated")
        highlight = kind in {"for_you", "daily"} or item.get("weekday") == today_wd
        if source == "later":
            count = _table_count("listen_later")
            shelves.append({
                "kind": kind,
                "title": title,
                "subtitle": subtitle,
                "playlist_id": None,
                "tracks": count,
                "ready": count > 0,
                "highlight": highlight,
                "cover_track_id": None,
                "special": "later",
            })
            continue
        if source == "favorites":
            count = _table_count("favorites")
            shelves.append({
                "kind": kind,
                "title": title,
                "subtitle": subtitle,
                "playlist_id": None,
                "tracks": count,
                "ready": count > 0,
                "highlight": highlight,
                "cover_track_id": None,
                "special": "favorites",
            })
            continue
        playlist = latest_playlist(kind)
        track_count = len(playlist["tracks"]) if playlist else 0
        shelves.append({
            "kind": kind,
            "title": title,
            "subtitle": subtitle,
            "playlist_id": playlist["id"] if playlist else None,
            "tracks": track_count,
            "ready": playlist is not None and track_count > 0,
            "highlight": highlight,
            "cover_track_id": playlist["tracks"][0]["track_id"] if playlist and playlist["tracks"] else None,
        })
    return shelves


def _table_count(table: str) -> int:
    if table not in {"listen_later", "favorites"}:
        raise ValueError(table)
    with connect() as conn:
        row = conn.execute(
            f"SELECT COUNT(*) AS c FROM {table} WHERE profile_id = :profile",
            {"profile": conn.profile_id},
        ).fetchone()
    return int(row["c"]) if row else 0
