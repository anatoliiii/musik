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
    DEFAULT_MODEL_PATH,
    load_model,
    write_model_atomic,
)
from musik.config import get_settings
from musik.db.schema import connect, utcnow
from musik.db.scoped_connection import active_profile

MIN_LABELS = 1500
MIN_PER_CLASS = 200
MIN_HOLDOUT = 200
RETRAIN_NEW_LABELS = 200
GAP = timedelta(days=3)
L2 = 1.0
METRICS_SCHEMA_VERSION = 1
EXCLUDED_SOURCES = {"manual"}
EXCLUDED_OUTCOMES = {"partial", "superseded", "abandoned", "pending"}


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


def load_labeled_impressions() -> list[dict[str, Any]]:
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT i.impression_id, i.source, i.outcome, i.played_at, i.closed_at,
                   i.queued_at, i.legacy, i.features_json,
                   (
                       SELECT h.action FROM (SELECT * FROM listening_history WHERE profile_id=:musik_profile) h
                       WHERE h.impression_id = i.impression_id
                         AND h.action IN ('like', 'dislike')
                       ORDER BY h.ts DESC LIMIT 1
                   ) AS explicit_action
            FROM (SELECT * FROM recommendation_impressions WHERE profile_id=:musik_profile) i
            WHERE i.legacy = 0
              AND i.played_at IS NOT NULL
            ORDER BY COALESCE(i.closed_at, i.played_at, i.queued_at)
            """
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
        labeled.append({"label": label, "features": features, "ts": ts, "row": item})
    return labeled


def last_completed_label_count() -> int:
    with connect() as conn:
        row = conn.execute(
            """
            SELECT positive_count, negative_count
            FROM (SELECT * FROM training_runs WHERE profile_id=:musik_profile) AS training_runs
            WHERE model_type='ranker' AND status='completed'
            ORDER BY created_at DESC LIMIT 1
            """
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
    x: np.ndarray, y: np.ndarray, *, l2: float = L2, iters: int = 40
) -> tuple[np.ndarray, float, np.ndarray, np.ndarray]:
    mean = x.mean(axis=0)
    std = x.std(axis=0)
    std[std < 1e-6] = 1.0
    xs = (x - mean) / std
    n, dim = xs.shape
    weights = np.zeros(dim, dtype=np.float64)
    bias = 0.0
    for _ in range(iters):
        z = xs @ weights + bias
        p = _sigmoid(z)
        w = np.clip(p * (1.0 - p), 1e-6, None)
        err = p - y
        grad_w = (xs.T @ err) / n + l2 * weights / n
        grad_b = float(err.mean())
        hess = (xs.T * w) @ xs / n + l2 * np.eye(dim) / n
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


def log_loss(y: np.ndarray, scores: np.ndarray) -> float:
    p = np.clip(_sigmoid(scores), 1e-6, 1 - 1e-6)
    return float(-np.mean(y * np.log(p) + (1 - y) * np.log(1 - p)))


def roc_auc(y: np.ndarray, scores: np.ndarray) -> float:
    order = np.argsort(scores)
    y = y[order]
    n_pos = float(y.sum())
    n_neg = float(len(y) - n_pos)
    if n_pos == 0 or n_neg == 0:
        return 0.5
    ranks = np.arange(1, len(y) + 1, dtype=np.float64)
    return float((ranks[y == 1].sum() - n_pos * (n_pos + 1) / 2) / (n_pos * n_neg))


def expected_calibration_error(y: np.ndarray, scores: np.ndarray, bins: int = 8) -> float:
    p = _sigmoid(scores)
    edges = np.linspace(0, 1, bins + 1)
    total = 0.0
    n = len(y)
    for lo, hi in zip(edges[:-1], edges[1:], strict=True):
        mask = (p >= lo) & (p < hi if hi < 1 else p <= hi)
        if not np.any(mask):
            continue
        total += (mask.sum() / n) * abs(float(y[mask].mean()) - float(p[mask].mean()))
    return float(total)


def evaluate(y: np.ndarray, scores: np.ndarray) -> dict[str, float]:
    return {
        "log_loss": log_loss(y, scores),
        "auc": roc_auc(y, scores),
        "ece": expected_calibration_error(y, scores),
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
) -> None:
    now = utcnow()
    with connect() as conn:
        conn.execute(
            """
            INSERT INTO training_runs(
              run_id, model_version, model_type, feature_schema_version,
              train_from, train_until, positive_count, negative_count,
              metrics_schema_version, metrics_json, status, created_at, completed_at
            ,profile_id) VALUES (?, ?, 'ranker', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,:musik_profile)
            """,
            (
                run_id,
                model_version,
                FEATURE_SCHEMA_VERSION,
                train_from,
                train_until,
                positive,
                negative,
                METRICS_SCHEMA_VERSION,
                json.dumps(metrics, ensure_ascii=False),
                status,
                now,
                now,
            ),
        )


def _activate_model(model_version: str, path: Path, digest: str) -> None:
    now = utcnow()
    with connect() as conn:
        conn.execute(
            "UPDATE model_versions SET status='retired' WHERE model_versions.profile_id=:musik_profile AND ( model_type='ranker' AND status='active') "
        )
        conn.execute(
            """
            INSERT INTO model_versions(
              model_version, model_type, feature_schema_version, artifact_path,
              artifact_hash, status, created_at, activated_at
            ,profile_id) VALUES (?, 'ranker', ?, ?, ?, 'active', ?, ?,:musik_profile)
            """,
            (model_version, FEATURE_SCHEMA_VERSION, str(path), digest, now, now),
        )


def train_ranker(*, force: bool = False, artifact_path: Path | None = None) -> dict[str, Any]:
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
    x_hold = np.asarray([row["features"] for row in holdout], dtype=np.float64)
    y_hold = np.asarray([row["label"] for row in holdout], dtype=np.float64)
    if y_train.min() == y_train.max() or y_hold.min() == y_hold.max():
        result = {"status": "rejected", "reason": "single_class_split"}
        _record_run(
            run_id=run_id, model_version=None, status="rejected",
            train_from=train_from, train_until=train_until,
            positive=positives, negative=negatives, metrics=result,
        )
        return result

    weights, bias, mean, std = fit_logistic(x_train, y_train)
    learned_scores = _scores(weights, bias, mean, std, x_hold)
    default_hold = default_scores(x_hold)
    learned = evaluate(y_hold, learned_scores)
    baseline = evaluate(y_hold, default_hold)
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


def default_model_path() -> Path:
    return DEFAULT_MODEL_PATH
