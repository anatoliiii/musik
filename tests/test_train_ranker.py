from __future__ import annotations

from datetime import datetime, timedelta, timezone

import numpy as np

from musik.brain.ranker import FEATURE_ORDER, load_model, write_model_atomic
from musik.brain.train_ranker import (
    evaluate,
    fit_logistic,
    impression_label,
    parse_features,
    time_split,
    train_ranker,
)
from musik.config import get_settings
from musik.db.schema import init_db


def test_impression_label_priority_and_exclusions() -> None:
    assert impression_label({"legacy": 1, "played_at": "now", "outcome": "finished"}) is None
    assert impression_label({"source": "manual", "played_at": "now", "outcome": "finished"}) is None
    assert impression_label({"played_at": "now", "outcome": "partial"}) is None
    assert impression_label({"played_at": "now", "outcome": "finished", "explicit_action": "dislike"}) == 0
    assert impression_label({"played_at": "now", "outcome": "early_skip", "explicit_action": "like"}) == 1
    assert impression_label({"played_at": "now", "outcome": "finished"}) == 1
    assert impression_label({"played_at": "now", "outcome": "early_skip"}) == 0


def test_parse_features_requires_schema_match() -> None:
    model = load_model()
    snap = {
        "schema_version": 1,
        "names": FEATURE_ORDER,
        "values": [0.0] * len(FEATURE_ORDER),
    }
    assert parse_features(__import__("json").dumps(snap)) == [0.0] * len(FEATURE_ORDER)
    snap["schema_version"] = 9
    assert parse_features(__import__("json").dumps(snap)) is None
    assert model.schema_version == 1


def test_time_split_keeps_gap() -> None:
    start = datetime(2026, 1, 1, tzinfo=timezone.utc)
    rows = [{"ts": start + timedelta(days=i), "label": i % 2} for i in range(20)]
    train, holdout = time_split(rows, gap=timedelta(days=3))
    assert train
    if holdout:
        assert holdout[0]["ts"] >= train[-1]["ts"] + timedelta(days=3)


def test_logistic_improves_separable_holdout() -> None:
    rng = np.random.default_rng(0)
    x = rng.normal(size=(80, 4))
    y = (x[:, 0] + 0.3 * x[:, 1] > 0).astype(np.float64)
    weights, bias, mean, std = fit_logistic(x[:60], y[:60])
    scores = ((x[60:] - mean) / std) @ weights + bias
    metrics = evaluate(y[60:], scores)
    assert metrics["auc"] > 0.7


def test_train_ranker_rejects_small_dataset(monkeypatch, tmp_path, test_database) -> None:
    test_database(tmp_path / "musik.db")
    monkeypatch.setenv("MUSIK_DATA_DIR", str(tmp_path))
    get_settings.cache_clear()
    init_db()
    result = train_ranker(force=True, artifact_path=tmp_path / "models" / "ranker.json")
    assert result["status"] == "rejected"
    assert result["reason"] in {"no_labeled_impressions", "insufficient_labels"}
    get_settings.cache_clear()


def test_write_model_atomic_roundtrip(tmp_path) -> None:
    model = load_model()
    path = tmp_path / "ranker.json"
    write_model_atomic(
        path,
        {
            "schema_version": model.schema_version,
            "model_version": "test-v1",
            "feature_order": model.feature_order,
            "mean": model.mean,
            "std": model.std,
            "weights": model.weights,
            "bias": model.bias,
            "score_min": model.score_min,
            "score_max": model.score_max,
        },
    )
    loaded = load_model(path)
    assert loaded.model_version == "test-v1"
