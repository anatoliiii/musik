"""Time-split logistic ranker training with guardrails and atomic publish."""

from __future__ import annotations

import hashlib
import json
import uuid
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

import numpy as np

from musik.brain.ranker import (
    FEATURE_ORDER,
    FEATURE_SCHEMA_VERSION,
    SPEC,
    load_model,
    write_model_atomic,
)
from musik.config import get_settings
from musik.db.schema import connect, utcnow
from musik.db.scoped_connection import active_profile

MIN_LABELS = 1500
MIN_TRANSITION_LABELS = 1500
MIN_PER_CLASS = 200
MIN_HOLDOUT = 200
RETRAIN_NEW_LABELS = 200
GAP = timedelta(days=3)
L2 = 1.0
METRICS_SCHEMA_VERSION = 1
PROPENSITY_FLOOR = 0.05
TRANSITION_SCHEMA_VERSION = 1
PAIRWISE_FEATURES = ["clap", "outcome", "bpm", "lufs", "key", "energy", "repeat"]
EXCLUDED_SOURCES = {"manual"}
EXCLUDED_OUTCOMES = {"partial", "superseded", "abandoned", "pending"}
_EXPLORE_SOURCES = {"explore_adjacent", "resurface", "new_in_library", "wildcard", "explore"}


def _parse_ts(value: str | None) -> datetime | None:
    if not value:
        return None
    raw = str(value).replace("Z", "+00:00")
    try:
        ts = datetime.fromisoformat(raw)
    except ValueError:
        return None
    if ts.tzinfo is None:
        ts = ts.replace(tzinfo=timezone.utc)
    return ts


def impression_label(row: dict[str, Any]) -> int | None:
    if int(row.get("legacy") or 0):
        return None
    if (row.get("source") or "") in EXCLUDED_SOURCES:
        return None
    if not row.get("played_at"):
        return None
    explicit = row.get("explicit_action")
    if explicit == "dislike":
        return 0
    if explicit == "like":
        return 1
    outcome = row.get("outcome") or ""
    if outcome in EXCLUDED_OUTCOMES:
        return None
    if outcome == "finished":
        return 1
    if outcome == "early_skip":
        return 0
    return None


def parse_features(raw: str | None) -> list[float] | None:
    if not raw:
        return None
    try:
        snap = json.loads(raw)
    except json.JSONDecodeError:
        return None
    if int(snap.get("schema_version") or 0) != FEATURE_SCHEMA_VERSION:
        return None
    names = list(snap.get("names") or [])
    values = list(snap.get("values") or [])
    if names != FEATURE_ORDER or len(values) != len(FEATURE_ORDER):
        return None
    return [float(v) for v in values]


def propensity_weight(sampled_p: float | None, source: str) -> float:
    """IPS weight from the logged bandit draw.

    Explore arms were shown with probability sampled_p. Everything else was
    shown with 1 - sampled_p. A missing or idle bandit keeps weight 1, which
    is the static quota policy. Probabilities are floored so one rare draw
    cannot dominate the fit.
    """
    if sampled_p is None:
        return 1.0
    try:
        explore_p = float(sampled_p)
    except (TypeError, ValueError):
        return 1.0
    if explore_p <= 0.0 or explore_p >= 1.0:
        return 1.0
    if source in _EXPLORE_SOURCES:
        p = explore_p
    else:
        p = 1.0 - explore_p
    p = min(1.0, max(p, PROPENSITY_FLOOR))
    return 1.0 / p


def _json_obj(raw: str | None) -> dict[str, Any]:
    if not raw:
        return {}
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        return {}
    return parsed if isinstance(parsed, dict) else {}


def sampled_propensity(features_json: str | None, policy_json: str | None) -> float | None:
    context = _json_obj(features_json).get("context") or {}
    if isinstance(context, dict) and context.get("sampled_p") not in (None, "", 0, 0.0):
        try:
            return float(context["sampled_p"])
        except (TypeError, ValueError):
            return None
    policy = _json_obj(policy_json)
    if policy.get("sampled_p") in (None, "", 0, 0.0):
        return None
    try:
        return float(policy["sampled_p"])
    except (TypeError, ValueError):
        return None


def load_labeled_impressions() -> list[dict[str, Any]]:
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT i.impression_id, i.source, i.position, i.outcome, i.played_at, i.closed_at,
                   i.queued_at, i.legacy, i.features_json, r.policy_json,
                   (
                       SELECT h.action FROM listening_history h
                       WHERE h.profile_id = i.profile_id
                         AND h.impression_id = i.impression_id
                         AND h.action IN ('like', 'dislike')
                       ORDER BY h.ts DESC LIMIT 1
                   ) AS explicit_action
            FROM recommendation_impressions i
            LEFT JOIN recommendation_requests r
              ON r.request_id = i.request_id AND r.profile_id = i.profile_id
            WHERE i.profile_id = :profile
              AND i.legacy = 0
              AND i.played_at IS NOT NULL
            ORDER BY COALESCE(i.closed_at, i.played_at, i.queued_at)
            """,
            {"profile": conn.profile_id},
        ).fetchall()
    labeled: list[dict[str, Any]] = []
    for row in rows:
        item = dict(row)
        label = impression_label(item)
        features = parse_features(item.get("features_json"))
        if label is None or features is None:
            continue
        ts = _parse_ts(item.get("closed_at") or item.get("played_at") or item.get("queued_at"))
        if ts is None:
            continue
        weight = propensity_weight(
            sampled_propensity(item.get("features_json"), item.get("policy_json")),
            str(item.get("source") or ""),
        )
        labeled.append({"label": label, "features": features, "ts": ts, "weight": weight, "row": item})
    return labeled


def last_completed_label_count(model_type: str = "ranker") -> int:
    with connect() as conn:
        row = conn.execute(
            """
            SELECT positive_count, negative_count
            FROM training_runs
            WHERE profile_id = :profile AND model_type = :model_type AND status='completed'
            ORDER BY created_at DESC LIMIT 1
            """,
            {"profile": conn.profile_id, "model_type": model_type},
        ).fetchone()
    if row is None:
        return 0
    return int(row["positive_count"]) + int(row["negative_count"])


def time_split(
    rows: list[dict[str, Any]], gap: timedelta = GAP
) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    if len(rows) < 4:
        return rows, []
    cut = rows[int(len(rows) * 0.8)]["ts"]
    train = [row for row in rows if row["ts"] <= cut]
    holdout = [row for row in rows if row["ts"] >= cut + gap]
    return train, holdout


def _sigmoid(z: np.ndarray) -> np.ndarray:
    z = np.clip(z, -20.0, 20.0)
    return 1.0 / (1.0 + np.exp(-z))


def fit_logistic(
    x: np.ndarray,
    y: np.ndarray,
    *,
    l2: float = L2,
    iters: int = 40,
    sample_weight: np.ndarray | None = None,
) -> tuple[np.ndarray, float, np.ndarray, np.ndarray]:
    x = np.asarray(x, dtype=np.float64)
    y = np.asarray(y, dtype=np.float64)
    n = len(y)
    if sample_weight is None:
        w_i = np.ones(n, dtype=np.float64)
    else:
        w_i = np.clip(np.asarray(sample_weight, dtype=np.float64), 0.0, None)
        if len(w_i) != n:
            raise ValueError("sample_weight length")
    wsum = float(w_i.sum())
    if wsum <= 0:
        w_i = np.ones(n, dtype=np.float64)
        wsum = float(n)
    mean = (w_i[:, None] * x).sum(axis=0) / wsum
    var = (w_i[:, None] * (x - mean) ** 2).sum(axis=0) / wsum
    std = np.sqrt(var)
    std[std < 1e-6] = 1.0
    xs = (x - mean) / std
    dim = xs.shape[1]
    weights = np.zeros(dim, dtype=np.float64)
    bias = 0.0
    for _ in range(iters):
        z = xs @ weights + bias
        p = _sigmoid(z)
        hess_w = np.clip(p * (1.0 - p), 1e-6, None) * w_i
        err = (p - y) * w_i
        grad_w = (xs.T @ err) / wsum + l2 * weights / wsum
        grad_b = float(err.sum() / wsum)
        hess = (xs.T * hess_w) @ xs / wsum + l2 * np.eye(dim) / wsum
        try:
            step = np.linalg.solve(hess, grad_w)
        except np.linalg.LinAlgError:
            step = grad_w
        weights -= step
        weights = np.clip(weights, -8.0, 8.0)
        bias = float(np.clip(bias - grad_b, -4.0, 4.0))
    return weights, bias, mean, std


def _scores(model_weights: np.ndarray, bias: float, mean: np.ndarray, std: np.ndarray, x: np.ndarray) -> np.ndarray:
    return ((x - mean) / std) @ model_weights + bias


def _weights(y: np.ndarray, sample_weight: np.ndarray | None) -> np.ndarray:
    if sample_weight is None:
        return np.ones(len(y), dtype=np.float64)
    return np.clip(np.asarray(sample_weight, dtype=np.float64), 0.0, None)


def log_loss(y: np.ndarray, scores: np.ndarray, sample_weight: np.ndarray | None = None) -> float:
    p = np.clip(_sigmoid(scores), 1e-6, 1 - 1e-6)
    loss = -(y * np.log(p) + (1 - y) * np.log(1 - p))
    w = _weights(y, sample_weight)
    denom = float(w.sum())
    if denom <= 0:
        return float(np.mean(loss))
    return float(np.sum(w * loss) / denom)


def roc_auc(y: np.ndarray, scores: np.ndarray, sample_weight: np.ndarray | None = None) -> float:
    order = np.argsort(scores)
    y = np.asarray(y, dtype=np.float64)[order]
    w = _weights(y, None if sample_weight is None else np.asarray(sample_weight)[order])
    n_pos = float((w * y).sum())
    n_neg = float((w * (1.0 - y)).sum())
    if n_pos == 0 or n_neg == 0:
        return 0.5
    if sample_weight is None:
        ranks = np.arange(1, len(y) + 1, dtype=np.float64)
        return float((ranks[y == 1].sum() - n_pos * (n_pos + 1) / 2) / (n_pos * n_neg))
    acc = 0.0
    cum_neg = 0.0
    for label, weight in zip(y, w, strict=True):
        if label == 0:
            cum_neg += weight
        else:
            acc += weight * cum_neg
    return float(acc / (n_pos * n_neg))


def expected_calibration_error(
    y: np.ndarray, scores: np.ndarray, bins: int = 8, sample_weight: np.ndarray | None = None
) -> float:
    p = _sigmoid(scores)
    w = _weights(y, sample_weight)
    total_w = float(w.sum())
    if total_w <= 0:
        return 0.0
    edges = np.linspace(0, 1, bins + 1)
    total = 0.0
    for lo, hi in zip(edges[:-1], edges[1:], strict=True):
        mask = (p >= lo) & (p < hi if hi < 1 else p <= hi)
        if not np.any(mask):
            continue
        bin_w = float(w[mask].sum())
        if bin_w <= 0:
            continue
        y_mean = float((w[mask] * y[mask]).sum() / bin_w)
        p_mean = float((w[mask] * p[mask]).sum() / bin_w)
        total += (bin_w / total_w) * abs(y_mean - p_mean)
    return float(total)


def evaluate(
    y: np.ndarray, scores: np.ndarray, sample_weight: np.ndarray | None = None
) -> dict[str, float]:
    return {
        "log_loss": log_loss(y, scores, sample_weight),
        "auc": roc_auc(y, scores, sample_weight),
        "ece": expected_calibration_error(y, scores, sample_weight=sample_weight),
    }


def default_scores(x: np.ndarray) -> np.ndarray:
    model = load_model()
    out = []
    for row in x:
        out.append(model.score(row.tolist()))
    return np.asarray(out, dtype=np.float64)


def _record_run(
    *,
    run_id: str,
    model_version: str | None,
    status: str,
    train_from: str,
    train_until: str,
    positive: int,
    negative: int,
    metrics: dict[str, Any],
    model_type: str = "ranker",
    schema_version: int = FEATURE_SCHEMA_VERSION,
) -> None:
    now = utcnow()
    with connect() as conn:
        conn.execute(
            """
            INSERT INTO training_runs(
              run_id, model_version, model_type, feature_schema_version,
              train_from, train_until, positive_count, negative_count,
              metrics_schema_version, metrics_json, status, created_at, completed_at
            , profile_id) VALUES (
              :run_id, :model_version, :model_type, :feature_schema_version,
              :train_from, :train_until, :positive, :negative,
              :metrics_schema_version, :metrics, :status, :now, :now, :profile
            )
            """,
            {
                "run_id": run_id, "model_version": model_version, "model_type": model_type,
                "feature_schema_version": schema_version,
                "train_from": train_from, "train_until": train_until,
                "positive": positive, "negative": negative,
                "metrics_schema_version": METRICS_SCHEMA_VERSION,
                "metrics": json.dumps(metrics, ensure_ascii=False),
                "status": status, "now": now, "profile": conn.profile_id,
            },
        )


def _activate_model(
    model_version: str,
    path: Path,
    digest: str,
    *,
    model_type: str = "ranker",
    schema_version: int = FEATURE_SCHEMA_VERSION,
) -> None:
    now = utcnow()
    with connect() as conn:
        conn.execute(
            "UPDATE model_versions SET status='retired' "
            "WHERE profile_id = :profile AND model_type = :model_type AND status='active'",
            {"profile": conn.profile_id, "model_type": model_type},
        )
        conn.execute(
            """
            INSERT INTO model_versions(
              model_version, model_type, feature_schema_version, artifact_path,
              artifact_hash, status, created_at, activated_at
            , profile_id) VALUES (
              :model_version, :model_type, :feature_schema_version, :path, :digest, 'active', :now, :now, :profile
            )
            """,
            {"model_version": model_version, "model_type": model_type,
             "feature_schema_version": schema_version,
             "path": str(path), "digest": digest, "now": now, "profile": conn.profile_id},
        )


def _train_unary_ranker(*, force: bool = False, artifact_path: Path | None = None) -> dict[str, Any]:
    rows = load_labeled_impressions()
    positives = sum(row["label"] for row in rows)
    negatives = len(rows) - positives
    run_id = str(uuid.uuid4())
    if not rows:
        result = {"status": "rejected", "reason": "no_labeled_impressions"}
        _record_run(
            run_id=run_id, model_version=None, status="rejected",
            train_from="", train_until="", positive=0, negative=0, metrics=result,
        )
        return result
    train_from = rows[0]["ts"].isoformat()
    train_until = rows[-1]["ts"].isoformat()
    previous = last_completed_label_count()
    if not force and previous and len(rows) - previous < RETRAIN_NEW_LABELS:
        result = {
            "status": "rejected",
            "reason": "not_enough_new_labels",
            "labeled": len(rows),
            "previous": previous,
        }
        _record_run(
            run_id=run_id, model_version=None, status="rejected",
            train_from=train_from, train_until=train_until,
            positive=positives, negative=negatives, metrics=result,
        )
        return result
    if len(rows) < MIN_LABELS or positives < MIN_PER_CLASS or negatives < MIN_PER_CLASS:
        result = {
            "status": "rejected",
            "reason": "insufficient_labels",
            "labeled": len(rows),
            "positives": positives,
            "negatives": negatives,
            "min_labels": MIN_LABELS,
            "min_per_class": MIN_PER_CLASS,
        }
        _record_run(
            run_id=run_id, model_version=None, status="rejected",
            train_from=train_from, train_until=train_until,
            positive=positives, negative=negatives, metrics=result,
        )
        return result

    train, holdout = time_split(rows)
    if len(holdout) < MIN_HOLDOUT:
        result = {
            "status": "rejected",
            "reason": "small_holdout",
            "holdout": len(holdout),
            "min_holdout": MIN_HOLDOUT,
        }
        _record_run(
            run_id=run_id, model_version=None, status="rejected",
            train_from=train_from, train_until=train_until,
            positive=positives, negative=negatives, metrics=result,
        )
        return result

    x_train = np.asarray([row["features"] for row in train], dtype=np.float64)
    y_train = np.asarray([row["label"] for row in train], dtype=np.float64)
    w_train = np.asarray([row["weight"] for row in train], dtype=np.float64)
    x_hold = np.asarray([row["features"] for row in holdout], dtype=np.float64)
    y_hold = np.asarray([row["label"] for row in holdout], dtype=np.float64)
    w_hold = np.asarray([row["weight"] for row in holdout], dtype=np.float64)
    if y_train.min() == y_train.max() or y_hold.min() == y_hold.max():
        result = {"status": "rejected", "reason": "single_class_split"}
        _record_run(
            run_id=run_id, model_version=None, status="rejected",
            train_from=train_from, train_until=train_until,
            positive=positives, negative=negatives, metrics=result,
        )
        return result

    weights, bias, mean, std = fit_logistic(x_train, y_train, sample_weight=w_train)
    learned_scores = _scores(weights, bias, mean, std, x_hold)
    default_hold = default_scores(x_hold)
    learned = evaluate(y_hold, learned_scores, w_hold)
    baseline = evaluate(y_hold, default_hold, w_hold)
    guardrails = {
        "log_loss_ok": learned["log_loss"] <= baseline["log_loss"] + 1e-6,
        "auc_ok": learned["auc"] >= baseline["auc"] - 1e-6,
        "ece_ok": learned["ece"] <= baseline["ece"] + 0.01,
    }
    metrics = {
        "learned": learned,
        "default": baseline,
        "guardrails": guardrails,
        "train_size": len(train),
        "holdout_size": len(holdout),
        "class_balance": {"positive": positives, "negative": negatives},
        "normalized_weights": {
            name: float(w) for name, w in zip(FEATURE_ORDER, weights, strict=True)
        },
        "raw_weights": {
            name: float(w / std[i]) for i, (name, w) in enumerate(zip(FEATURE_ORDER, weights, strict=True))
        },
        "bias": float(bias),
    }
    if not all(guardrails.values()):
        metrics["status"] = "rejected"
        metrics["reason"] = "guardrail_regression"
        _record_run(
            run_id=run_id, model_version=None, status="rejected",
            train_from=train_from, train_until=train_until,
            positive=positives, negative=negatives, metrics=metrics,
        )
        return {"status": "rejected", "reason": "guardrail_regression", **metrics}

    default = load_model()
    model_version = datetime.now(timezone.utc).strftime("learned-%Y%m%dT%H%M%SZ")
    payload = {
        "schema_version": FEATURE_SCHEMA_VERSION,
        "model_version": model_version,
        "feature_order": FEATURE_ORDER,
        "mean": [float(v) for v in mean],
        "std": [float(v) for v in std],
        "weights": [float(v) for v in weights],
        "bias": float(bias),
        "score_min": default.score_min,
        "score_max": default.score_max,
        "train": metrics,
    }
    settings = get_settings()
    path = artifact_path or (settings.data_dir / "models" / "ranker.json")
    if artifact_path is None and settings.multi_user:
        profile_id = active_profile.get()
        if profile_id is None:
            with connect() as conn:
                profile_id = conn.profile_id
        path = settings.data_dir / "models" / "profiles" / profile_id / "ranker.json"
    write_model_atomic(path, payload)
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    _activate_model(model_version, path, digest)
    _record_run(
        run_id=run_id, model_version=model_version, status="completed",
        train_from=train_from, train_until=train_until,
        positive=positives, negative=negatives, metrics=metrics,
    )
    return {
        "status": "completed",
        "model_version": model_version,
        "artifact_path": str(path),
        "artifact_hash": digest,
        **metrics,
    }


def train_ranker(*, force: bool = False, artifact_path: Path | None = None) -> dict[str, Any]:
    result = _train_unary_ranker(force=force, artifact_path=artifact_path)
    transition_path = None if artifact_path is None else artifact_path.with_name("transition.json")
    result["transition"] = train_transition(force=force, artifact_path=transition_path)
    return result


def transition_label(row: dict[str, Any]) -> int | None:
    """1 when the queued next track was finished. Skips are 0. Likes are taste, not a transition."""
    if int(row.get("legacy") or 0):
        return None
    if (row.get("source") or "") in EXCLUDED_SOURCES:
        return None
    if not row.get("played_at"):
        return None
    outcome = row.get("outcome") or ""
    if outcome == "finished":
        return 1
    if outcome == "early_skip":
        return 0
    return None


def parse_pairwise(raw: str | None) -> tuple[dict[str, float], str] | None:
    snap = _json_obj(raw)
    if int(snap.get("schema_version") or 0) != FEATURE_SCHEMA_VERSION:
        return None
    context = snap.get("context") or {}
    if not isinstance(context, dict):
        return None
    raw_feats = context.get("pairwise") or {}
    if not isinstance(raw_feats, dict):
        return None
    if any(name not in raw_feats for name in PAIRWISE_FEATURES):
        return None
    features = {name: float(raw_feats[name]) for name in PAIRWISE_FEATURES}
    profile = str(context.get("profile") or "smooth")
    if profile not in SPEC.profiles:
        profile = "smooth"
    return features, profile


def manual_transition_score(features: dict[str, float], profile: str) -> float:
    weights = SPEC.profiles.get(profile) or SPEC.profiles["smooth"]
    return (
        weights["clap"] * features["clap"]
        + weights["outcome"] * features["outcome"]
        + weights["key"] * features["key"]
        + weights["energy"] * features["energy"]
        - weights["bpm"] * features["bpm"]
        - weights["lufs"] * features["lufs"]
        - weights["repeat"] * features["repeat"]
    )


def load_transition_examples() -> list[dict[str, Any]]:
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT i.source, i.outcome, i.played_at, i.closed_at, i.queued_at,
                   i.legacy, i.features_json, r.policy_json
            FROM recommendation_impressions i
            LEFT JOIN recommendation_requests r
              ON r.request_id = i.request_id AND r.profile_id = i.profile_id
            WHERE i.profile_id = :profile
              AND i.legacy = 0
              AND i.played_at IS NOT NULL
            ORDER BY COALESCE(i.closed_at, i.played_at, i.queued_at)
            """,
            {"profile": conn.profile_id},
        ).fetchall()
    labeled: list[dict[str, Any]] = []
    for row in rows:
        item = dict(row)
        label = transition_label(item)
        parsed = parse_pairwise(item.get("features_json"))
        if label is None or parsed is None:
            continue
        features, profile = parsed
        ts = _parse_ts(item.get("closed_at") or item.get("played_at") or item.get("queued_at"))
        if ts is None:
            continue
        labeled.append({
            "label": label,
            "features": [features[name] for name in PAIRWISE_FEATURES],
            "manual": manual_transition_score(features, profile),
            "ts": ts,
            "weight": propensity_weight(
                sampled_propensity(item.get("features_json"), item.get("policy_json")),
                str(item.get("source") or ""),
            ),
        })
    return labeled


def _transition_artifact_path(artifact_path: Path | None) -> Path:
    if artifact_path is not None:
        return artifact_path
    settings = get_settings()
    path = settings.data_dir / "models" / "transition.json"
    if not settings.multi_user:
        return path
    profile_id = active_profile.get()
    if profile_id is None:
        with connect() as conn:
            profile_id = conn.profile_id
    return settings.data_dir / "models" / "profiles" / profile_id / "transition.json"


def train_transition(*, force: bool = False, artifact_path: Path | None = None) -> dict[str, Any]:
    """Fit the pairwise finish model. Hand weights stay until enough labels pass the guardrail."""
    rows = load_transition_examples()
    positives = sum(row["label"] for row in rows)
    negatives = len(rows) - positives
    run_id = str(uuid.uuid4())
    train_from = rows[0]["ts"].isoformat() if rows else ""
    train_until = rows[-1]["ts"].isoformat() if rows else ""

    def reject(result: dict[str, Any]) -> dict[str, Any]:
        result.setdefault("fallback", "manual_weights")
        _record_run(
            run_id=run_id, model_version=None, status="rejected",
            train_from=train_from, train_until=train_until,
            positive=positives, negative=negatives, metrics=result,
            model_type="transition", schema_version=TRANSITION_SCHEMA_VERSION,
        )
        return result

    if not rows:
        return reject({"status": "rejected", "reason": "no_pairwise_labels"})
    previous = last_completed_label_count("transition")
    if not force and previous and len(rows) - previous < RETRAIN_NEW_LABELS:
        return reject({
            "status": "rejected",
            "reason": "not_enough_new_labels",
            "labeled": len(rows),
            "previous": previous,
        })
    if len(rows) < MIN_TRANSITION_LABELS or positives < MIN_PER_CLASS or negatives < MIN_PER_CLASS:
        return reject({
            "status": "rejected",
            "reason": "insufficient_labels",
            "labeled": len(rows),
            "positives": positives,
            "negatives": negatives,
            "min_labels": MIN_TRANSITION_LABELS,
            "min_per_class": MIN_PER_CLASS,
        })
    train, holdout = time_split(rows)
    if len(holdout) < MIN_HOLDOUT:
        return reject({"status": "rejected", "reason": "small_holdout", "holdout": len(holdout)})
    x_train = np.asarray([row["features"] for row in train], dtype=np.float64)
    y_train = np.asarray([row["label"] for row in train], dtype=np.float64)
    w_train = np.asarray([row["weight"] for row in train], dtype=np.float64)
    x_hold = np.asarray([row["features"] for row in holdout], dtype=np.float64)
    y_hold = np.asarray([row["label"] for row in holdout], dtype=np.float64)
    w_hold = np.asarray([row["weight"] for row in holdout], dtype=np.float64)
    manual_hold = np.asarray([row["manual"] for row in holdout], dtype=np.float64)
    if y_train.min() == y_train.max() or y_hold.min() == y_hold.max():
        return reject({"status": "rejected", "reason": "single_class_split"})
    weights, bias, mean, std = fit_logistic(x_train, y_train, sample_weight=w_train)
    learned_scores = _scores(weights, bias, mean, std, x_hold)
    learned = evaluate(y_hold, learned_scores, w_hold)
    baseline = evaluate(y_hold, manual_hold, w_hold)
    guardrails = {
        "log_loss_ok": learned["log_loss"] <= baseline["log_loss"] + 1e-6,
        "auc_ok": learned["auc"] >= baseline["auc"] - 1e-6,
        "ece_ok": learned["ece"] <= baseline["ece"] + 0.01,
    }
    metrics = {
        "learned": learned,
        "manual": baseline,
        "guardrails": guardrails,
        "train_size": len(train),
        "holdout_size": len(holdout),
        "fallback": "manual_weights",
    }
    if not all(guardrails.values()):
        metrics["status"] = "rejected"
        metrics["reason"] = "guardrail_regression"
        return reject(metrics)
    model_version = datetime.now(timezone.utc).strftime("transition-%Y%m%dT%H%M%SZ")
    payload = {
        "schema_version": TRANSITION_SCHEMA_VERSION,
        "model_version": model_version,
        "feature_order": PAIRWISE_FEATURES,
        "mean": [float(v) for v in mean],
        "std": [float(v) for v in std],
        "weights": [float(v) for v in weights],
        "bias": float(bias),
        "score_min": -5.0,
        "score_max": 5.0,
        "train": metrics,
    }
    path = _transition_artifact_path(artifact_path)
    write_model_atomic(path, payload)
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    _activate_model(
        model_version, path, digest,
        model_type="transition", schema_version=TRANSITION_SCHEMA_VERSION,
    )
    metrics["status"] = "completed"
    metrics["fallback"] = "learned"
    _record_run(
        run_id=run_id, model_version=model_version, status="completed",
        train_from=train_from, train_until=train_until,
        positive=positives, negative=negatives, metrics=metrics,
        model_type="transition", schema_version=TRANSITION_SCHEMA_VERSION,
    )
    return {
        "status": "completed",
        "model_version": model_version,
        "artifact_path": str(path),
        "artifact_hash": digest,
        "fallback": "learned",
        **metrics,
    }
