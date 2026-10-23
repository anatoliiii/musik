from __future__ import annotations

from datetime import datetime, timedelta, timezone

import numpy as np

from musik.brain.ranker import FEATURE_ORDER, FEATURE_SCHEMA_VERSION, load_model, write_model_atomic
from musik.brain.train_ranker import (
    evaluate,
    fit_logistic,
    impression_label,
    manual_transition_score,
    parse_features,
    parse_pairwise,
    propensity_weight,
    time_split,
    train_ranker,
    transition_label,
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


def test_propensity_weight_inverts_logged_draw() -> None:
    assert propensity_weight(None, "exploit") == 1.0
    assert propensity_weight(0.0, "wildcard") == 1.0
    assert propensity_weight(0.25, "wildcard") == 4.0
    assert propensity_weight(0.25, "exploit") == 1.0 / 0.75
    assert propensity_weight(0.01, "resurface") == 20.0


def test_transition_label_is_finish_not_like() -> None:
    assert transition_label({"played_at": "now", "outcome": "finished"}) == 1
    assert transition_label({"played_at": "now", "outcome": "early_skip"}) == 0
    assert transition_label({"played_at": "now", "outcome": "early_skip", "explicit_action": "like"}) == 0
    assert transition_label({"played_at": "now", "outcome": "partial"}) is None
    assert transition_label({"source": "manual", "played_at": "now", "outcome": "finished"}) is None


def test_parse_pairwise_requires_beam_features() -> None:
    snap = {
        "schema_version": FEATURE_SCHEMA_VERSION,
        "context": {
            "profile": "smooth",
            "pairwise": {name: 0.1 for name in ("clap", "outcome", "bpm", "lufs", "key", "energy", "repeat")},
        },
    }
    parsed = parse_pairwise(__import__("json").dumps(snap))
    assert parsed is not None
    features, profile = parsed
    assert profile == "smooth"
    assert manual_transition_score(features, profile) != 0
    snap["context"]["pairwise"].pop("clap")
    assert parse_pairwise(__import__("json").dumps(snap)) is None


def test_parse_features_requires_schema_match() -> None:
    model = load_model()
    snap = {
        "schema_version": FEATURE_SCHEMA_VERSION,
        "names": FEATURE_ORDER,
        "values": [0.0] * len(FEATURE_ORDER),
    }
    assert parse_features(__import__("json").dumps(snap)) == [0.0] * len(FEATURE_ORDER)
    snap["schema_version"] = 1
    assert parse_features(__import__("json").dumps(snap)) is None
    snap["schema_version"] = 2
    assert parse_features(__import__("json").dumps(snap)) is None
    assert model.schema_version == FEATURE_SCHEMA_VERSION


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
    assert result["transition"]["status"] == "rejected"
    assert result["transition"]["fallback"] == "manual_weights"
    assert result["transition"]["reason"] == "no_pairwise_labels"
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
