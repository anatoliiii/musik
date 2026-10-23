"""Shared feature schema, linear score, quotas and MMR for Go↔Python parity."""

from __future__ import annotations

import json
import math
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Iterable

# Active feature contract (v3). v2 added listening/impression fields; v3 fixes
# impression_fatigue to shows minus radio completions (library plays do not clear it).
# Training drops snapshots with schema_version < FEATURE_SCHEMA_VERSION.
FEATURE_SCHEMA_VERSION = 3
SPEC_PATH = (
    Path(__file__).resolve().parents[3] / "player" / "internal" / "queue" / "ranker_spec.json"
)


@dataclass(frozen=True)
class RankerSpec:
    feature_order: list[str]
    exploit_taste: float
    exploit_centroid: float
    transition_current: float
    adjacent_taste: float
    resurface_hours: float
    resurface_min_finishes: int
    new_hours: float
    new_max_plays: int
    quotas: dict[str, tuple[int, int]]
    mmr_lambda: float
    artist_penalty: float
    source_pool: int
    bpm_window: float
    key_min: float
    negative_clearance: float
    new_boost_days: float
    new_boost_beta: float
    new_boost_tau: float
    new_boost_gamma: float
    new_boost_skip_cap: int
    profiles: dict[str, dict[str, float]]


def _quota_map(raw: dict[str, Any]) -> dict[str, tuple[int, int]]:
    return {name: (int(item["min"]), int(item["max"])) for name, item in raw.items()}


def load_spec(path: Path | None = None) -> RankerSpec:
    raw = json.loads((path or SPEC_PATH).read_text(encoding="utf-8"))
    if int(raw["schema_version"]) != FEATURE_SCHEMA_VERSION:
        raise ValueError("ranker spec schema_version does not match feature schema")
    thresholds = raw["thresholds"]
    resurface = raw["resurface"]
    fresh = raw["new_in_library"]
    retrieval = raw["retrieval"]
    boost = raw["new_boost"]
    return RankerSpec(
        feature_order=list(raw["feature_order"]),
        exploit_taste=float(thresholds["exploit_taste"]),
        exploit_centroid=float(thresholds["exploit_centroid"]),
        transition_current=float(thresholds["transition_current"]),
        adjacent_taste=float(thresholds["adjacent_taste"]),
        resurface_hours=float(resurface["min_hours"]),
        resurface_min_finishes=int(resurface["min_finishes"]),
        new_hours=float(fresh["max_age_hours"]),
        new_max_plays=int(fresh["max_plays"]),
        quotas=_quota_map(raw["quotas"]),
        mmr_lambda=float(raw["mmr_lambda"]),
        artist_penalty=float(raw["artist_penalty"]),
        source_pool=int(retrieval["source_pool"]),
        bpm_window=float(retrieval["bpm_window"]),
        key_min=float(retrieval["key_min"]),
        negative_clearance=float(retrieval["negative_clearance"]),
        new_boost_days=float(boost["days"]),
        new_boost_beta=float(boost["beta"]),
        new_boost_tau=float(boost["tau_days"]),
        new_boost_gamma=float(boost["gamma"]),
        new_boost_skip_cap=int(boost["skip_early_cap"]),
        profiles={name: {key: float(value) for key, value in weights.items()} for name, weights in raw["transition_profiles"].items()},
    )


SPEC = load_spec()
FEATURE_ORDER = list(SPEC.feature_order)

SOURCE_PRIORITY = [
    "exploit",
    "transition",
    "explore_adjacent",
    "resurface",
    "new_in_library",
    "wildcard",
]

DEFAULT_QUOTAS = dict(SPEC.quotas)

DEFAULT_MODEL_PATH = (
    Path(__file__).resolve().parents[3]
    / "player"
    / "internal"
    / "queue"
    / "model_default.json"
)


@dataclass(frozen=True)
class Model:
    schema_version: int
    model_version: str
    feature_order: list[str]
    mean: list[float]
    std: list[float]
    weights: list[float]
    bias: float
    score_min: float
    score_max: float

    def validate(self) -> None:
        if self.schema_version != FEATURE_SCHEMA_VERSION:
            raise ValueError("model schema_version does not match feature schema")
        if self.feature_order != FEATURE_ORDER:
            raise ValueError("feature_order mismatch")
        n = len(FEATURE_ORDER)
        if not (len(self.weights) == len(self.mean) == len(self.std) == n):
            raise ValueError("model vector lengths must match feature_order")
        if any(std == 0 for std in self.std):
            raise ValueError("std must be non-zero")
        if self.score_max <= self.score_min:
            raise ValueError("score bounds invalid")

    def score(self, values: list[float]) -> float:
        if len(values) != len(self.weights):
            return self.bias
        total = self.bias
        for value, weight, mean, std in zip(values, self.weights, self.mean, self.std, strict=True):
            total += weight * (value - mean) / std
        return min(self.score_max, max(self.score_min, total))


def load_model(path: Path | None = None) -> Model:
    raw = json.loads((path or DEFAULT_MODEL_PATH).read_text())
    model = Model(
        schema_version=int(raw["schema_version"]),
        model_version=str(raw["model_version"]),
        feature_order=list(raw["feature_order"]),
        mean=[float(x) for x in raw["mean"]],
        std=[float(x) for x in raw["std"]],
        weights=[float(x) for x in raw["weights"]],
        bias=float(raw["bias"]),
        score_min=float(raw["score_min"]),
        score_max=float(raw["score_max"]),
    )
    model.validate()
    return model


EXPLORE_SOURCES = (
    "explore_adjacent",
    "resurface",
    "new_in_library",
    "wildcard",
)


def primary_source(sources: Iterable[str]) -> str:
    have = set(sources)
    for source in SOURCE_PRIORITY:
        if source in have:
            return source
    return "wildcard"


def label_sources(
    *,
    sim_taste: float,
    sim_current: float = 0.0,
    sim_centroid_max: float = 0.0,
    plays: int = 0,
    finishes: int = 0,
    hours_since_finished: float | None = None,
    hours_since_created: float | None = None,
) -> list[str]:
    out: list[str] = []
    if sim_taste >= SPEC.exploit_taste or sim_centroid_max >= SPEC.exploit_centroid:
        out.append("exploit")
    if sim_current >= SPEC.transition_current:
        out.append("transition")
    if SPEC.adjacent_taste <= sim_taste < SPEC.exploit_taste:
        out.append("explore_adjacent")
    if (
        hours_since_finished is not None
        and hours_since_finished >= SPEC.resurface_hours
        and finishes >= SPEC.resurface_min_finishes
    ):
        out.append("resurface")
    if hours_since_created is not None and hours_since_created <= SPEC.new_hours and plays < SPEC.new_max_plays:
        out.append("new_in_library")
    if sim_taste < SPEC.adjacent_taste:
        out.append("wildcard")
    return out or ["wildcard"]


def feature_vector(
    *,
    sim_taste: float = 0.0,
    sim_current: float = 0.0,
    sim_session: float = 0.0,
    sim_daypart: float = 0.0,
    sim_mood: float = 0.0,
    sim_place: float = 0.0,
    sim_activity: float = 0.0,
    sim_centroid_max: float = 0.0,
    transition_weight: float = 0.0,
    transition_score: float = 0.0,
    bpm_distance: float = 0.0,
    bpm_missing: float = 1.0,
    lufs_diff: float = 0.0,
    lufs_missing: float = 1.0,
    key_compat: float = 0.0,
    energy_diff: float = 0.0,
    artist_repeat: float = 0.0,
    album_repeat: float = 0.0,
    new_boost: float = 0.0,
    plays: int = 0,
    finishes: int = 0,
    partial: int = 0,
    likes: int = 0,
    dislikes: int = 0,
    shown: int = 0,
    completed: int = 0,
    early_skip_rate: float = 0.0,
    hours_since_played: float = 0.0,
    hours_since_finished: float = 0.0,
    never_played: float = 1.0,
    negative_penalty: float = 0.0,
    duration_min: float = 0.0,
    queue_position: float = 0.0,
) -> list[float]:
    """Model features. Source labels stay a quota policy and are not included."""
    values = {
        "sim_taste": sim_taste,
        "sim_current": sim_current,
        "sim_session": sim_session,
        "sim_daypart": sim_daypart,
        "sim_mood": sim_mood,
        "sim_place": sim_place,
        "sim_activity": sim_activity,
        "sim_centroid_max": sim_centroid_max,
        "transition_weight": transition_weight,
        "transition_score": transition_score,
        "bpm_distance": bpm_distance,
        "bpm_missing": bpm_missing,
        "lufs_diff": lufs_diff,
        "lufs_missing": lufs_missing,
        "key_compat": key_compat,
        "energy_diff": energy_diff,
        "artist_repeat": artist_repeat,
        "album_repeat": album_repeat,
        "new_boost": new_boost,
        "plays_log": math.log1p(plays),
        "early_skip_rate": early_skip_rate,
        "hours_since_played": hours_since_played,
        "never_played": never_played,
        "negative_penalty": negative_penalty,
        "likes_log": math.log1p(likes),
        "dislikes_log": math.log1p(dislikes),
        "finish_rate": _count_rate(finishes, plays),
        "partial_rate": _count_rate(partial, plays),
        "impression_fatigue": _impression_fatigue(shown, completed),
        "hours_since_finished": hours_since_finished,
        "duration_min": duration_min,
        "queue_position": queue_position,
    }
    return [float(values[name]) for name in FEATURE_ORDER]


def scale_quotas(
    size: int,
    base: dict[str, tuple[int, int]] | None = None,
    explore_ratio: float | None = None,
) -> dict[str, tuple[int, int]]:
    size = max(size, 1)
    base = base or DEFAULT_QUOTAS
    if explore_ratio is not None and explore_ratio <= 0:
        out = {source: (lo, hi) for source, (lo, hi) in base.items()}
        for source in EXPLORE_SOURCES:
            out[source] = (0, 0)
        out["exploit"] = (max(1, size - 1), size)
        return out
    scale = size / 6
    out: dict[str, tuple[int, int]] = {}
    mins = 0
    for source in SOURCE_PRIORITY:
        lo, hi = base[source]
        min_n = int(math.floor(lo * scale + 1e-9))
        max_n = int(round(hi * scale))
        if lo > 0 and min_n < 1 and size >= 3:
            min_n = 1
        if max_n < min_n:
            max_n = min_n
        out[source] = (min_n, max_n)
        mins += min_n
    for source in reversed(SOURCE_PRIORITY):
        if mins <= size:
            break
        lo, hi = out[source]
        if lo > 0:
            out[source] = (lo - 1, hi)
            mins -= 1
    return out


def mmr_select(
    scores: list[float],
    vectors: list[list[float]],
    artists: list[str],
    *,
    size: int,
    lambda_: float | None = None,
    artist_penalty: float | None = None,
    forbidden: Iterable[int] | None = None,
) -> list[int]:
    if lambda_ is None:
        lambda_ = SPEC.mmr_lambda
    if artist_penalty is None:
        artist_penalty = SPEC.artist_penalty
    remaining = set(range(len(scores)))
    if forbidden:
        remaining -= set(forbidden)
    selected: list[int] = []
    counts: dict[str, int] = {}
    while remaining and len(selected) < size:
        best_i = None
        best = -1e9
        for i in remaining:
            div = 0.0
            if selected:
                div = max(_dot(vectors[i], vectors[j]) for j in selected)
            artist = (artists[i] or "").strip().lower()
            value = lambda_ * scores[i] - (1.0 - lambda_) * div - artist_penalty * counts.get(artist, 0)
            if value > best:
                best = value
                best_i = i
        if best_i is None:
            break
        selected.append(best_i)
        remaining.remove(best_i)
        artist = (artists[best_i] or "").strip().lower()
        counts[artist] = counts.get(artist, 0) + 1
    return selected


def _labels(item: dict[str, Any]) -> list[str]:
    sources = item.get("sources")
    if sources:
        return [str(src) for src in sources]
    source = str(item.get("source") or "")
    return [source] if source else ["wildcard"]


def _quota_slot(
    item: dict[str, Any],
    counts: dict[str, int],
    quotas: dict[str, tuple[int, int]],
) -> str | None:
    """The arm this track should occupy. Mirrors Go quotaSlot."""
    labels = _labels(item)
    for source in reversed(SOURCE_PRIORITY):
        lo, _hi = quotas.get(source, (0, 0))
        if source in labels and counts.get(source, 0) < lo:
            return source
    primary = str(item.get("source") or "")
    if primary and primary in labels:
        _lo, hi = quotas.get(primary, (0, 0))
        if counts.get(primary, 0) < hi:
            return primary
    for source in EXPLORE_SOURCES:
        if source != primary and source in labels:
            _lo, hi = quotas.get(source, (0, 0))
            if counts.get(source, 0) < hi:
                return source
    return None


def apply_quotas(
    items: list[dict[str, Any]],
    *,
    size: int,
    quotas: dict[str, tuple[int, int]] | None = None,
) -> list[dict[str, Any]]:
    """Fill minimums from the scarce end of the arm list, then open maxima.

    A track is filed under every label. The arm that takes it is written back
    as ``source``, so a forgotten hit that is also close to taste can close
    resurface instead of disappearing into exploit.
    """
    quotas = quotas or scale_quotas(size)
    ranked = sorted(items, key=lambda x: (-float(x["score"]), int(x.get("track_id", 0))))
    by_source: dict[str, list[dict[str, Any]]] = {src: [] for src in SOURCE_PRIORITY}
    for item in ranked:
        for source in _labels(item):
            by_source.setdefault(source, []).append(item)
    chosen: list[dict[str, Any]] = []
    used: set[int] = set()
    counts: dict[str, int] = {src: 0 for src in SOURCE_PRIORITY}
    for source in reversed(SOURCE_PRIORITY):
        want = quotas.get(source, (0, 0))[0]
        for item in by_source.get(source, []):
            if len(chosen) >= size or counts[source] >= want:
                break
            tid = int(item["track_id"])
            if tid in used:
                continue
            taken = dict(item)
            taken["source"] = source
            chosen.append(taken)
            used.add(tid)
            counts[source] += 1
    for item in ranked:
        if len(chosen) >= size:
            break
        tid = int(item["track_id"])
        if tid in used:
            continue
        slot = _quota_slot(item, counts, quotas)
        if slot is None:
            continue
        taken = dict(item)
        taken["source"] = slot
        chosen.append(taken)
        used.add(tid)
        counts[slot] = counts.get(slot, 0) + 1
    return chosen


def _count_rate(part: int, total: int) -> float:
    if total <= 0:
        return 0.0
    return part / total


def _impression_fatigue(shown: int, completed: int) -> float:
    """Shows that were not finished from radio. Library plays do not clear them."""
    gap = shown - completed
    if gap <= 0:
        return 0.0
    return math.log1p(gap)


def outcome_transition_delta(outcome: str, manual: bool) -> float:
    delta = {"finished": 1.0, "partial": 0.3, "early_skip": -0.1}.get(outcome, 0.0)
    if manual:
        delta *= 1.5
    return delta


def _dot(a: list[float], b: list[float]) -> float:
    return float(sum(x * y for x, y in zip(a, b, strict=False)))


def write_model_atomic(path: Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f".{path.name}.tmp")
    tmp.write_text(json.dumps(payload, indent=2, ensure_ascii=False), encoding="utf-8")
    tmp.replace(path)


def _parse_time(value: Any) -> datetime | None:
    if value is None:
        return None
    text = str(value).strip()
    if not text:
        return None
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        parsed = datetime.fromisoformat(text)
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    return parsed.astimezone(timezone.utc)


def _hours_since(value: Any, now: datetime) -> float | None:
    then = _parse_time(value)
    if then is None:
        return None
    hours = (now - then).total_seconds() / 3600
    return max(0.0, hours)


def _capped_hours(value: Any, now: datetime) -> tuple[float, bool]:
    hours = _hours_since(value, now)
    if hours is None:
        return 0.0, True
    return min(hours / 720, 4.0), False


def _opt_float(value: Any) -> float | None:
    if value is None or value == "":
        return None
    return float(value)


def _same_credit(left: Any, right: Any) -> bool:
    a = str(left or "").strip()
    b = str(right or "").strip()
    return bool(a) and a.casefold() == b.casefold()


def _camelot(key: str) -> int:
    text = str(key or "").strip()
    if not text:
        return 0
    upper = text.upper()
    number = 0
    for char in upper:
        if char.isdigit():
            number = number * 10 + int(char)
        elif number > 0:
            break
    if number < 1 or number > 12:
        return 0
    mode = 12 if "B" in upper or "maj" in text.lower() else 0
    return number + mode


def _key_neighbors(a: int, b: int) -> bool:
    mode_a, mode_b = a > 12, b > 12
    na, nb = (a - 12 if a > 12 else a), (b - 12 if b > 12 else b)
    if mode_a == mode_b:
        return abs(na - nb) % 12 == 1 or abs(na - nb) == 11
    return na == nb


def _key_compat(current: dict[str, Any] | None, track: dict[str, Any]) -> float:
    if current is None:
        return 0.0
    a, b = _camelot(str(current.get("key") or "")), _camelot(str(track.get("key") or ""))
    if a == 0 or b == 0:
        return 0.0
    if a == b:
        return 1.0
    if _key_neighbors(a, b):
        return 0.7
    return 0.15


def _almost_multiple(slow: float, fast: float, factor: float) -> bool:
    return abs(fast - factor * slow) / max(fast, 1.0) < 0.08


def _clamp01(value: float) -> float:
    return min(1.0, max(0.0, value))


def _bpm_distance(current: dict[str, Any] | None, track: dict[str, Any]) -> tuple[float, float]:
    cur = _opt_float(None if current is None else current.get("bpm"))
    nxt = _opt_float(track.get("bpm"))
    if cur is None or nxt is None or cur <= 0 or nxt <= 0:
        return 0.0, 1.0
    slow, fast = (cur, nxt) if cur <= nxt else (nxt, cur)
    rel = abs(fast - slow) / fast
    if _almost_multiple(slow, fast, 2):
        rel = min(rel, abs(fast - 2 * slow) / (2 * slow))
    return _clamp01(rel), 0.0


def _lufs_distance(current: dict[str, Any] | None, track: dict[str, Any]) -> tuple[float, float]:
    cur = _opt_float(None if current is None else current.get("lufs"))
    nxt = _opt_float(track.get("lufs"))
    if cur is None or nxt is None:
        return 0.0, 1.0
    return abs(cur - nxt) / 20, 0.0


def _energy_diff(current: dict[str, Any] | None, track: dict[str, Any]) -> float:
    cur = _opt_float(None if current is None else current.get("lufs"))
    nxt = _opt_float(track.get("lufs"))
    if cur is None or nxt is None:
        return 0.0
    return (nxt - cur) / 10


def _profile_weights(profile: str) -> dict[str, float]:
    return SPEC.profiles.get(profile) or SPEC.profiles["smooth"]


def transition_score(
    current: dict[str, Any] | None,
    track: dict[str, Any],
    clap: float,
    outcome: float,
    profile: str = "smooth",
) -> tuple[float, float, float]:
    """Artist repeat, album repeat, and the pairwise score Go writes into the vector."""
    if not current or not int(current.get("id") or 0):
        return 0.0, 0.0, 0.0
    artist = 1.0 if _same_credit(current.get("artist"), track.get("artist")) else 0.0
    album = 1.0 if _same_credit(current.get("album"), track.get("album")) else 0.0
    weights = _profile_weights(profile)
    bpm, _missing = _bpm_distance(current, track)
    lufs, _lufs_missing = _lufs_distance(current, track)
    key = _key_compat(current, track)
    energy = _energy_diff(current, track)
    repeat = artist + 0.5 * album
    score = (
        weights["clap"] * clap
        + weights["outcome"] * outcome
        + weights["key"] * key
        + weights["energy"] * energy
        - weights["bpm"] * bpm
        - weights["lufs"] * lufs
        - weights["repeat"] * repeat
    )
    return artist, album, score


def spec_new_boost(track: dict[str, Any], now: datetime) -> float:
    if int(track.get("skip_early") or 0) >= SPEC.new_boost_skip_cap:
        return 0.0
    created = _hours_since(track.get("created_at"), now)
    if created is None:
        return 0.0
    age_days = created / 24
    if age_days > SPEC.new_boost_days:
        return 0.0
    shown = int(track.get("shown") or 0)
    return SPEC.new_boost_beta * math.exp(-age_days / SPEC.new_boost_tau) * math.exp(-shown / SPEC.new_boost_gamma)


def _norm_transition(weight: float, transitions: dict[int, float] | None) -> float:
    if weight <= 0 or not transitions:
        return 0.0
    max_w = max(transitions.values(), default=0.0)
    if max_w <= 0:
        return 0.0
    return math.log1p(weight) / math.log1p(max_w)


def vector_from_track(
    track: dict[str, Any],
    *,
    current: dict[str, Any] | None = None,
    sim_taste: float = 0.0,
    sim_current: float = 0.0,
    sim_session: float = 0.0,
    sim_daypart: float = 0.0,
    sim_mood: float = 0.0,
    sim_place: float = 0.0,
    sim_activity: float = 0.0,
    sim_centroid_max: float = 0.0,
    transitions: dict[int, float] | None = None,
    profile: str = "smooth",
    now: datetime | None = None,
    negative_penalty: float = 0.0,
    new_boost: float | None = None,
) -> list[float]:
    """The same feature vector BuildCore writes for one candidate."""
    now = now or datetime.now(timezone.utc)
    plays = int(track.get("plays") or 0)
    finishes = int(track.get("finishes") or 0)
    partial = int(track.get("partial") or 0)
    likes = int(track.get("likes") or 0)
    dislikes = int(track.get("dislikes") or 0)
    shown = int(track.get("shown") or 0)
    completed = int(track.get("completed") or 0)
    early = int(track.get("early_skips") or 0)
    played_hours, played_missing = _capped_hours(track.get("last_played_at"), now)
    finished_hours, _finished_missing = _capped_hours(track.get("last_finished_at"), now)
    bpm, bpm_missing = _bpm_distance(current, track)
    lufs, lufs_missing = _lufs_distance(current, track)
    raw_weight = 0.0
    if transitions:
        raw_weight = float(transitions.get(int(track.get("id") or 0), 0.0))
    artist, album, score = transition_score(current, track, sim_current, raw_weight, profile)
    if new_boost is None:
        new_boost = spec_new_boost(track, now)
    return feature_vector(
        sim_taste=sim_taste,
        sim_current=sim_current,
        sim_session=sim_session,
        sim_daypart=sim_daypart,
        sim_mood=sim_mood,
        sim_place=sim_place,
        sim_activity=sim_activity,
        sim_centroid_max=sim_centroid_max,
        transition_weight=_norm_transition(raw_weight, transitions),
        transition_score=score,
        bpm_distance=bpm,
        bpm_missing=bpm_missing,
        lufs_diff=lufs,
        lufs_missing=lufs_missing,
        key_compat=_key_compat(current, track),
        energy_diff=_energy_diff(current, track),
        artist_repeat=artist,
        album_repeat=album,
        new_boost=new_boost,
        plays=plays,
        finishes=finishes,
        partial=partial,
        likes=likes,
        dislikes=dislikes,
        shown=shown,
        completed=completed,
        early_skip_rate=(early / plays) if plays else 0.0,
        hours_since_played=0.0 if played_missing else played_hours,
        hours_since_finished=finished_hours,
        never_played=1.0 if played_missing else 0.0,
        negative_penalty=negative_penalty,
        duration_min=float(track.get("duration_sec") or track.get("duration") or 0.0) / 60,
    )


def _unit(vec: Iterable[float]) -> list[float]:
    values = [float(x) for x in vec]
    norm = math.sqrt(sum(x * x for x in values))
    if norm <= 1e-12:
        return values
    return [x / norm for x in values]


def _cosine(a: Iterable[float], b: Iterable[float]) -> float:
    left, right = _unit(a), _unit(b)
    return _dot(left, right)


def rank_fixture(payload: dict[str, Any], model: Model | None = None) -> list[dict[str, Any]]:
    """Score the shared golden input the same way on both sides of the contract."""
    model = model or load_model()
    now = _parse_time(payload.get("now")) or datetime.now(timezone.utc)
    profile = str(payload.get("profile") or "smooth")
    current = payload.get("current")
    taste = payload.get("taste") or []
    transitions = {int(key): float(value) for key, value in (payload.get("transitions") or {}).items()}
    size = int(payload.get("size") or 6)
    items: list[dict[str, Any]] = []
    for track in payload.get("candidates") or []:
        sim_taste = _cosine(taste, track.get("vector") or [])
        sim_current = _cosine(current.get("vector") or [], track.get("vector") or []) if current else 0.0
        raw_finished = _hours_since(track.get("last_finished_at"), now)
        raw_created = _hours_since(track.get("created_at"), now)
        sources = label_sources(
            sim_taste=sim_taste,
            sim_current=sim_current,
            plays=int(track.get("plays") or 0),
            finishes=int(track.get("finishes") or 0),
            hours_since_finished=raw_finished,
            hours_since_created=raw_created,
        )
        features = vector_from_track(
            track,
            current=current,
            sim_taste=sim_taste,
            sim_current=sim_current,
            transitions=transitions,
            profile=profile,
            now=now,
            negative_penalty=float(track.get("negative_penalty") or 0.0),
        )
        items.append(
            {
                "track_id": int(track["id"]),
                "source": primary_source(sources),
                "sources": sources,
                "score": model.score(features),
                "features": features,
            }
        )
    return apply_quotas(items, size=size, quotas=scale_quotas(size))


def _matched_current(index: Any, current: Any) -> tuple[dict[str, Any] | None, int | None]:
    import numpy as np

    if current is None or index.size == 0:
        return None, None
    sims = index.sims_to_vector(np.asarray(current, dtype=np.float32))
    row = int(np.argmax(sims))
    if float(sims[row]) < 0.999:
        return None, None
    return index.meta[row], row


def _top_k(score: list[float], k: int, allow: Any) -> list[int]:
    if k < 1:
        return []
    ranked = sorted(
        ((row, sim) for row, sim in enumerate(score) if allow(row)),
        key=lambda item: (-item[1], item[0]),
    )
    return [row for row, _sim in ranked[:k]]


def _resurface_eligible(meta: dict[str, Any], now: datetime) -> bool:
    if int(meta.get("finishes") or 0) < SPEC.resurface_min_finishes:
        return False
    hours = _hours_since(meta.get("last_finished_at"), now)
    return hours is not None and hours >= SPEC.resurface_hours


def _new_eligible(meta: dict[str, Any], now: datetime) -> bool:
    if int(meta.get("plays") or 0) >= SPEC.new_max_plays:
        return False
    hours = _hours_since(meta.get("created_at"), now)
    return hours is not None and hours <= SPEC.new_hours


def _transition_rows(
    index: Any,
    current_meta: dict[str, Any] | None,
    current_sims: list[float],
    transitions: dict[int, float] | None,
    pool: int,
    allow: Any,
) -> list[int]:
    rows = _top_k(current_sims, pool, allow)
    seen = set(rows)
    if current_meta is not None:
        window = [-2.0] * index.size
        for row in range(index.size):
            if not allow(row) or row in seen:
                continue
            meta = index.meta[row]
            bpm, missing = _bpm_distance(current_meta, meta)
            key = _key_compat(current_meta, meta)
            if (missing == 0 and bpm <= SPEC.bpm_window) or key >= SPEC.key_min:
                window[row] = current_sims[row]
        for row in _top_k(window, pool, lambda row: window[row] > -1.5):
            if row not in seen:
                seen.add(row)
                rows.append(row)
    ranked: list[tuple[int, float]] = []
    for raw_id, weight in (transitions or {}).items():
        weight_f = float(weight)
        if weight_f <= 0:
            continue
        row = index.row_of(int(raw_id))
        if row is None or not allow(row) or row in seen:
            continue
        ranked.append((int(row), weight_f))
    ranked.sort(key=lambda item: (-item[1], item[0]))
    rows.extend(row for row, _weight in ranked[:pool])
    return rows


def _resurface_rows(index: Any, now: datetime, pool: int, allow: Any) -> list[int]:
    hits: list[tuple[int, int, datetime]] = []
    for row in range(index.size):
        if not allow(row):
            continue
        meta = index.meta[row]
        if not _resurface_eligible(meta, now):
            continue
        finished = _parse_time(meta.get("last_finished_at"))
        if finished is None:
            continue
        hits.append((row, int(meta.get("finishes") or 0), finished))
    hits.sort(key=lambda item: (-item[1], item[2], item[0]))
    return [row for row, _finishes, _when in hits[:pool]]


def _new_rows(index: Any, now: datetime, pool: int, allow: Any) -> list[int]:
    hits: list[tuple[int, datetime]] = []
    for row in range(index.size):
        if not allow(row):
            continue
        meta = index.meta[row]
        if not _new_eligible(meta, now):
            continue
        created = _parse_time(meta.get("created_at"))
        if created is None:
            continue
        hits.append((row, created))
    hits.sort(key=lambda item: (-item[1].timestamp(), item[0]))
    return [row for row, _created in hits[:pool]]


def _adjacent_rows(taste: list[float], pool: int, allow: Any) -> list[int]:
    lo, hi = SPEC.adjacent_taste, SPEC.exploit_taste
    score = [-2.0] * len(taste)
    for row, sim in enumerate(taste):
        if allow(row) and lo <= sim < hi:
            score[row] = sim
    return _top_k(score, pool, lambda row: score[row] > -1.5)


def _wildcard_rows(index: Any, taste: list[float], pool: int, allow: Any, negative: Any) -> list[int]:
    limit = SPEC.adjacent_taste
    clearance = SPEC.negative_clearance
    score = [-2.0] * index.size
    for row in range(index.size):
        taste_sim = taste[row] if row < len(taste) else 0.0
        if not allow(row) or taste_sim >= limit:
            continue
        if negative is not None and float(negative(index.matrix[row])) >= clearance:
            continue
        score[row] = -taste_sim
    return _top_k(score, pool, lambda row: score[row] > -1.5)


def _arm_rows(
    index: Any,
    *,
    taste_sims: list[float],
    current_sims: list[float],
    current_meta: dict[str, Any] | None,
    current_row: int | None,
    forbidden: set[int],
    transitions: dict[int, float] | None,
    now: datetime,
    pool: int,
    negative: Any,
) -> list[int]:
    md5s = list(getattr(index, "md5s", []) or [])
    current_md5 = ""
    if current_row is not None and 0 <= current_row < len(md5s):
        current_md5 = md5s[current_row] or ""

    def allow(row: int) -> bool:
        if row in forbidden or row == current_row:
            return False
        if current_md5 and row < len(md5s) and md5s[row] == current_md5:
            return False
        return True

    seen: set[int] = set()
    rows: list[int] = []

    def add(picked: list[int]) -> None:
        for row in picked:
            if row in seen:
                continue
            seen.add(row)
            rows.append(row)

    add(_top_k(taste_sims, pool, allow))
    add(_transition_rows(index, current_meta, current_sims, transitions, pool, allow))
    add(_resurface_rows(index, now, pool, allow))
    add(_new_rows(index, now, pool, allow))
    add(_adjacent_rows(taste_sims, pool, allow))
    add(_wildcard_rows(index, taste_sims, pool, allow, negative))
    return rows


def rank_index(
    index: Any,
    *,
    taste: Any,
    current: Any | None = None,
    forbidden: set[int] | None = None,
    size: int,
    explore_ratio: float | None = None,
    model: Model | None = None,
    pool: int | None = None,
    now: datetime | None = None,
    transitions: dict[int, float] | None = None,
    profile: str = "smooth",
    negative: Any = None,
) -> list[dict[str, Any]]:
    """Score an embedding index by arm, then apply the shared quotas.

    ``pool`` is the width of each arm. When omitted, retrieval.source_pool
    from the spec is used. The cosine top is no longer the only cutoff.
    """
    import numpy as np

    if index.size == 0 or size < 1:
        return []
    model = model or load_model()
    forbidden = forbidden or set()
    now = now or datetime.now(timezone.utc)
    arm_pool = SPEC.source_pool if pool is None or pool < 1 else int(pool)
    taste_vec = np.asarray(taste, dtype=np.float32)
    taste_sims = [float(x) for x in index.sims_to_vector(taste_vec)]
    if current is not None:
        current_sims = [float(x) for x in index.sims_to_vector(np.asarray(current, dtype=np.float32))]
    else:
        current_sims = taste_sims
    current_meta, current_row = _matched_current(index, current)
    rows = _arm_rows(
        index,
        taste_sims=taste_sims,
        current_sims=current_sims,
        current_meta=current_meta,
        current_row=current_row,
        forbidden=forbidden,
        transitions=transitions,
        now=now,
        pool=arm_pool,
        negative=negative,
    )
    items: list[dict[str, Any]] = []
    for row_i in rows:
        meta = index.meta[row_i]
        sim_taste = taste_sims[row_i]
        sim_current = current_sims[row_i]
        raw_finished = _hours_since(meta.get("last_finished_at"), now)
        raw_created = _hours_since(meta.get("created_at"), now)
        sources = label_sources(
            sim_taste=sim_taste,
            sim_current=sim_current,
            plays=int(meta.get("plays") or 0),
            finishes=int(meta.get("finishes") or 0),
            hours_since_finished=raw_finished,
            hours_since_created=raw_created,
        )
        features = vector_from_track(
            meta,
            current=current_meta,
            sim_taste=sim_taste,
            sim_current=sim_current,
            transitions=transitions,
            profile=profile,
            now=now,
        )
        items.append(
            {
                "track_id": int(meta["id"]),
                "row": row_i,
                "source": primary_source(sources),
                "sources": sources,
                "score": model.score(features),
                "features": features,
                "sim_taste": sim_taste,
                "sim_current": sim_current,
                "artist": meta.get("artist") or "",
                "vector": index.matrix[row_i].tolist(),
                "cluster_id": meta.get("cluster_id"),
            }
        )
    quotas = scale_quotas(size, explore_ratio=explore_ratio)
    chosen = apply_quotas(items, size=size, quotas=quotas)
    if len(chosen) < size:
        used = {int(item["track_id"]) for item in chosen}
        for item in items:
            if len(chosen) >= size:
                break
            if int(item["track_id"]) in used:
                continue
            chosen.append(item)
            used.add(int(item["track_id"]))
    return chosen[:size]
