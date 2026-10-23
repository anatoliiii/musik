import json
import math
from datetime import datetime, timezone
from pathlib import Path

from musik.brain.ranker import (
    FEATURE_ORDER,
    FEATURE_SCHEMA_VERSION,
    DEFAULT_MODEL_PATH,
    SPEC,
    SPEC_PATH,
    apply_quotas,
    feature_vector,
    label_sources,
    vector_from_track,
    load_model,
    mmr_select,
    outcome_transition_delta,
    rank_fixture,
    rank_index,
    scale_quotas,
)


def test_default_model_matches_go_artifact() -> None:
    model = load_model()
    assert model.schema_version == FEATURE_SCHEMA_VERSION
    assert model.model_version == "default-v3"
    assert model.feature_order == FEATURE_ORDER
    assert DEFAULT_MODEL_PATH.exists()
    values = [0.0] * len(FEATURE_ORDER)
    values[FEATURE_ORDER.index("sim_taste")] = 0.8
    values[FEATURE_ORDER.index("sim_current")] = 0.4
    score = model.score(values)
    assert 0.4 < score < 0.8


def test_quota_rounding_for_queue_size_six() -> None:
    quotas = scale_quotas(6)
    assert quotas["exploit"][0] == 2
    assert quotas["explore_adjacent"][0] == 1
    assert quotas["resurface"] == (1, 1)
    assert sum(lo for lo, _ in quotas.values()) <= 6
    closed = scale_quotas(6, explore_ratio=0)
    assert closed["explore_adjacent"] == (0, 0)
    assert closed["wildcard"] == (0, 0)


def test_mmr_penalizes_same_artist() -> None:
    scores = [1.0, 0.99, 0.5]
    vectors = [[1.0, 0.0], [0.99, 0.01], [0.0, 1.0]]
    artists = ["a", "a", "b"]
    selected = mmr_select(scores, vectors, artists, size=2, lambda_=0.6)
    assert selected[0] == 0
    assert selected[1] == 2


def test_outcome_transition_weights() -> None:
    assert outcome_transition_delta("finished", False) == 1.0
    assert outcome_transition_delta("partial", False) == 0.3
    assert outcome_transition_delta("early_skip", False) == -0.1
    assert outcome_transition_delta("finished", True) == 1.5


def test_spec_is_the_threshold_source() -> None:
    assert SPEC.exploit_taste == 0.45
    assert SPEC.transition_current == 0.55
    assert SPEC.adjacent_taste == 0.15
    assert SPEC.mmr_lambda == 0.7
    assert SPEC.quotas["exploit"] == (2, 4)
    assert SPEC.quotas["resurface"] == (1, 1)
    assert SPEC.source_pool == 48
    assert SPEC.profiles["smooth"]["clap"] == 0.4
    raw = json.loads(SPEC_PATH.read_text(encoding="utf-8"))
    assert raw["discover_quotas"]["resurface"]["min"] == 0


def test_golden_ranker_fixture() -> None:
    path = Path(__file__).parent / "golden" / "ranker_fixture.json"
    payload = json.loads(path.read_text(encoding="utf-8"))
    ranked = rank_fixture(payload)
    assert [item["track_id"] for item in ranked] == [item["track_id"] for item in payload["expected"]]
    for got, want in zip(ranked, payload["expected"], strict=True):
        assert got["source"] == want["source"]
        assert got["sources"] == want["sources"]
        assert len(got["features"]) == len(FEATURE_ORDER)
        for actual, expected in zip(got["features"], want["features"], strict=True):
            assert math.isclose(actual, expected, abs_tol=1e-6)
    loved = next(item for item in ranked if item["track_id"] == 10)
    assert loved["source"] == "resurface"
    assert "exploit" in loved["sources"]
    assert loved["features"][FEATURE_ORDER.index("artist_repeat")] == 1
    assert loved["features"][FEATURE_ORDER.index("likes_log")] == math.log1p(2)
    assert loved["features"][FEATURE_ORDER.index("never_played")] == 0
    assert loved["features"][FEATURE_ORDER.index("plays_log")] == math.log1p(4)
    assert loved["features"][FEATURE_ORDER.index("bpm_missing")] == 0
    assert loved["features"][FEATURE_ORDER.index("transition_score")] != 0
    fresh = next(item for item in ranked if item["track_id"] == 11)
    assert "new_in_library" in fresh["sources"]
    assert fresh["features"][FEATURE_ORDER.index("never_played")] == 1
    assert fresh["features"][FEATURE_ORDER.index("new_boost")] > 0


def test_rank_index_uses_listening_stats() -> None:
    import numpy as np

    from musik.index.brute import EmbeddingIndex

    now = "2026-10-19T12:00:00Z"
    meta = [
        {
            "id": 1,
            "artist": "Near",
            "album": "One",
            "title": "Seed",
            "path": "",
            "file_md5": "a",
            "vector_row": 0,
            "plays": 0,
            "bpm": 100,
            "lufs": -14,
            "key": "8A",
            "duration": 180,
        },
        {
            "id": 2,
            "artist": "Near",
            "album": "One",
            "title": "Next",
            "path": "",
            "file_md5": "b",
            "plays": 4,
            "finishes": 3,
            "likes": 2,
            "early_skips": 1,
            "shown": 4,
            "bpm": 104,
            "lufs": -12,
            "key": "8A",
            "duration": 200,
            "last_played_at": "2026-10-18T12:00:00Z",
            "last_finished_at": "2026-10-18T12:00:00Z",
        },
    ]
    mat = np.array([[1, 0], [1, 0]], dtype=np.float32)
    index = EmbeddingIndex(
        track_ids=np.array([1, 2], dtype=np.int64),
        matrix=mat,
        meta=meta,
        md5s=["a", "b"],
    )
    ranked = rank_index(
        index,
        taste=np.array([1, 0], dtype=np.float32),
        current=mat[0],
        forbidden={0},
        size=1,
        now=datetime.fromisoformat(now.replace("Z", "+00:00")),
        transitions={2: 2.0},
    )
    assert len(ranked) == 1
    values = ranked[0]["features"]
    assert values[FEATURE_ORDER.index("plays_log")] == math.log1p(4)
    assert values[FEATURE_ORDER.index("never_played")] == 0
    assert values[FEATURE_ORDER.index("likes_log")] == math.log1p(2)
    assert values[FEATURE_ORDER.index("artist_repeat")] == 1
    assert values[FEATURE_ORDER.index("bpm_missing")] == 0
    assert values[FEATURE_ORDER.index("transition_score")] != 0


def test_source_one_hot_is_not_a_model_feature() -> None:
    assert "source_exploit" not in FEATURE_ORDER
    assert "queue_position" in FEATURE_ORDER
    values = feature_vector(sim_taste=0.2)
    assert values[FEATURE_ORDER.index("queue_position")] == 0.0


def test_golden_feature_order_file() -> None:
    golden = Path(__file__).parent / "golden" / "feature_order_v3.txt"
    assert golden.read_text().splitlines() == FEATURE_ORDER


def test_schema_v3_listening_features_match_go_scale() -> None:
    values = feature_vector(
        plays=4,
        finishes=3,
        partial=1,
        likes=2,
        dislikes=1,
        shown=7,
        completed=1,
        hours_since_finished=1.0,
        duration_min=3.0,
    )
    assert len(values) == len(FEATURE_ORDER)

    def at(name: str) -> float:
        return values[FEATURE_ORDER.index(name)]

    assert at("likes_log") == math.log1p(2)
    assert at("dislikes_log") == math.log1p(1)
    assert at("finish_rate") == 0.75
    assert at("partial_rate") == 0.25
    assert at("impression_fatigue") == math.log1p(6)
    assert at("hours_since_finished") == 1.0
    assert at("duration_min") == 3.0
    assert "resurface" in label_sources(sim_taste=0.9, finishes=2, hours_since_finished=14 * 24)
    assert "resurface" not in label_sources(sim_taste=0.9, finishes=2, hours_since_finished=13 * 24)
    library = feature_vector(plays=40, shown=5, completed=1)
    assert library[FEATURE_ORDER.index("impression_fatigue")] == math.log1p(4)
    from_meta = vector_from_track({"id": 1, "plays": 40, "shown": 5, "completed": 1})
    assert from_meta[FEATURE_ORDER.index("impression_fatigue")] == math.log1p(4)


def test_best_resurface_dual_takes_reserved_slot() -> None:
    items = [
        {"track_id": 1, "source": "exploit", "sources": ["exploit", "resurface"], "score": 0.95},
        {"track_id": 8, "source": "exploit", "sources": ["exploit", "resurface"], "score": 0.5},
        {"track_id": 2, "source": "exploit", "sources": ["exploit"], "score": 0.9},
        {"track_id": 3, "source": "exploit", "sources": ["exploit"], "score": 0.85},
        {"track_id": 4, "source": "exploit", "sources": ["exploit"], "score": 0.8},
        {"track_id": 5, "source": "explore_adjacent", "sources": ["explore_adjacent"], "score": 0.4},
        {"track_id": 6, "source": "explore_adjacent", "sources": ["explore_adjacent"], "score": 0.3},
        {"track_id": 7, "source": "wildcard", "sources": ["wildcard"], "score": 0.1},
    ]
    chosen = apply_quotas(items, size=6)
    best = next(item for item in chosen if item["track_id"] == 1)
    assert best["source"] == "resurface"
    assert all(item["source"] != "resurface" for item in chosen if item["track_id"] != 1)
    closed = dict(scale_quotas(6))
    closed["resurface"] = (0, 0)
    hidden = apply_quotas(items, size=6, quotas=closed)
    again = next(item for item in hidden if item["track_id"] == 1)
    assert again["source"] == "exploit"


def test_rank_index_retrieves_each_arm_outside_cosine_top() -> None:
    import numpy as np

    from musik.index.brute import EmbeddingIndex

    now = datetime(2026, 10, 19, 12, tzinfo=timezone.utc)

    def unit(*xs: float) -> np.ndarray:
        arr = np.array(xs, dtype=np.float32)
        return arr / np.linalg.norm(arr)

    rows = [
        unit(1, 0),
        unit(1, 0),
        unit(1, 0),
        unit(1, 0),
        unit(1, 0),
        unit(1, 0),
        unit(0, 1),
        unit(0, 1),
        unit(1, 3),
        unit(0, 1),
        unit(0, 1),
        unit(0.6, 0.8),
    ]
    ids = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12]
    old = "2024-01-01T12:00:00Z"
    meta: list[dict] = []
    for track_id in ids:
        meta.append(
            {
                "id": track_id,
                "artist": f"A{track_id}",
                "album": "Album",
                "title": f"T{track_id}",
                "plays": 5,
                "finishes": 0,
                "created_at": old,
                "bpm": 180,
                "key": "1A",
                "duration": 180,
            }
        )
    meta[0].update(bpm=100, key="8A")
    meta[6].update(finishes=4, plays=6, last_finished_at="2026-08-01T12:00:00Z")
    meta[7].update(plays=0, created_at="2026-10-18T12:00:00Z")
    meta[10].update(bpm=100, key="8A")
    index = EmbeddingIndex(
        track_ids=np.array(ids, dtype=np.int64),
        matrix=np.stack(rows),
        meta=meta,
        md5s=[str(track_id) for track_id in ids],
    )
    ranked = rank_index(
        index,
        taste=np.array([1, 0], dtype=np.float32),
        current=index.matrix[0],
        forbidden={0},
        size=20,
        pool=2,
        now=now,
        transitions={10: 4.0},
    )
    by_id = {item["track_id"]: item for item in ranked}
    assert 12 not in by_id
    assert by_id[7]["source"] == "resurface"
    assert "exploit" not in by_id[7]["sources"]
    assert by_id[9]["source"] == "explore_adjacent"
    assert "new_in_library" in by_id[8]["sources"]
    assert 10 in by_id
    assert 11 in by_id


def test_wildcard_arm_skips_negative_neighbors() -> None:
    import numpy as np

    from musik.index.brute import EmbeddingIndex

    def unit(*xs: float) -> np.ndarray:
        arr = np.array(xs, dtype=np.float32)
        return arr / np.linalg.norm(arr)

    rows = [unit(1, 0), unit(1, 0), unit(1, 0), unit(0, 1), unit(0, -1)]
    ids = [1, 2, 3, 4, 5]
    meta = [
        {"id": track_id, "artist": "A", "album": "Album", "title": "T", "plays": 5, "created_at": "2020-01-01T00:00:00Z"}
        for track_id in ids
    ]
    index = EmbeddingIndex(
        track_ids=np.array(ids, dtype=np.int64),
        matrix=np.stack(rows),
        meta=meta,
        md5s=[str(track_id) for track_id in ids],
    )
    ranked = rank_index(
        index,
        taste=np.array([1, 0], dtype=np.float32),
        forbidden={0},
        size=10,
        pool=1,
        negative=lambda vec: 1.0 if float(vec[1]) < 0 else 0.0,
    )
    found = {item["track_id"] for item in ranked}
    assert 4 in found
    assert 5 not in found
