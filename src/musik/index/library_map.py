"""2D library map: UMAP over CLAP embeddings plus CLAP-text mood labels.

The map is a derived artifact that is always rebuilt and read whole, so it
lives as one JSON value in scan_state['library_map'] instead of its own table.
It covers every ready track; the player drops other profiles' private tracks
when it serves the map.
"""

from __future__ import annotations

import json
import logging
from collections import Counter
from dataclasses import dataclass

import numpy as np

from musik.db.schema import connect, utcnow

log = logging.getLogger(__name__)

MAP_KEY = "library_map"
MAP_VERSION = 1
LABEL_MIX_SIZE = 50
MAX_GENRES = 40

# (id, label, colour, CLAP prompt). English prompts: the text tower of
# larger_clap_music_and_speech was trained on English captions.
MOODS: tuple[tuple[str, str, str, str], ...] = (
    ("workout", "Тренировка", "#e4675a", "energetic intense workout music with a driving beat"),
    ("party", "Вечеринка", "#d97bb0", "upbeat dance party music"),
    ("happy", "Радостное", "#e2b13c", "happy cheerful uplifting music"),
    ("focus", "Фокус", "#7fb27a", "calm instrumental music for focus and concentration"),
    ("chill", "Чилл", "#5fb3a8", "chill relaxed laid-back music"),
    ("sleep", "Сон", "#a28ad8", "soft quiet ambient music for sleep"),
    ("sad", "Грустное", "#5a9fd8", "sad melancholic music"),
    ("heavy", "Драйв", "#c9824a", "aggressive heavy loud guitar music"),
)


@dataclass
class MapResult:
    n: int
    method: str
    labels: int
    genres: int


def _load_vectors() -> tuple[np.ndarray, np.ndarray]:
    """All ready embeddings regardless of profile (the player filters privacy)."""
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT t.id, f.embedding, f.embedding_dim
            FROM tracks t
            JOIN features f ON f.track_id = t.id
            WHERE t.is_active = 1
              AND t.is_duplicate_of IS NULL
              AND f.status = 'ready'
              AND f.embedding IS NOT NULL
            ORDER BY t.id
            """
        ).fetchall()
    if not rows:
        return np.zeros(0, dtype=np.int64), np.zeros((0, 0), dtype=np.float32)
    vectors = []
    for r in rows:
        vec = np.frombuffer(r["embedding"], dtype=np.float32)
        dim = int(r["embedding_dim"] or 0)
        if dim and vec.size > dim:
            vec = vec[:dim]
        vectors.append(vec)
    dim = max(v.size for v in vectors)
    mat = np.zeros((len(vectors), dim), dtype=np.float32)
    for i, v in enumerate(vectors):
        mat[i, : v.size] = v
    norms = np.linalg.norm(mat, axis=1, keepdims=True)
    mat /= np.maximum(norms, 1e-12)
    ids = np.array([int(r["id"]) for r in rows], dtype=np.int64)
    return ids, mat


def _track_genres(ids: np.ndarray) -> tuple[list[str], list[int]]:
    """One genre per track: of its tags, the one most common in the library."""
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT tg.track_id, g.name
            FROM track_genres tg
            JOIN genres g ON g.id = tg.genre_id
            """
        ).fetchall()
    wanted = set(ids.tolist())
    by_track: dict[int, list[str]] = {}
    counts: Counter[str] = Counter()
    for r in rows:
        tid = int(r["track_id"])
        name = (r["name"] or "").strip()
        if tid not in wanted or not name:
            continue
        by_track.setdefault(tid, []).append(name)
        counts[name] += 1
    genres = [name for name, _ in counts.most_common(MAX_GENRES)]
    slot = {name: i for i, name in enumerate(genres)}
    picks = []
    for tid in ids.tolist():
        names = [n for n in by_track.get(tid, []) if n in slot]
        picks.append(slot[max(names, key=lambda n: counts[n])] if names else -1)
    return genres, picks


def _pca(X: np.ndarray) -> np.ndarray:
    if X.shape[0] < 2:
        return np.zeros((X.shape[0], 2), dtype=np.float32)
    centered = X - X.mean(axis=0, keepdims=True)
    _u, _s, vt = np.linalg.svd(centered, full_matrices=False)
    return centered @ vt[:2].T


def _project(X: np.ndarray, *, seed: int) -> tuple[np.ndarray, str]:
    n = X.shape[0]
    if n >= 16:
        try:
            import umap
        except ImportError:
            log.warning("umap-learn is not installed; library map falls back to PCA")
        else:
            reducer = umap.UMAP(
                n_neighbors=min(15, n - 1),
                min_dist=0.1,
                metric="cosine",
                # A fixed seed keeps the layout stable between rebuilds, at
                # the cost of a single thread (minutes on 50k tracks).
                random_state=seed,
                n_jobs=1,
            )
            return np.asarray(reducer.fit_transform(X), dtype=np.float32), "umap"
    return _pca(X).astype(np.float32), "pca"


def _normalize(xy: np.ndarray) -> np.ndarray:
    """Fit into [0, 1]² keeping the aspect ratio, centred."""
    if xy.shape[0] == 0:
        return xy
    lo = xy.min(axis=0)
    span = xy.max(axis=0) - lo
    scale = float(span.max())
    if scale < 1e-12:
        return np.full_like(xy, 0.5)
    out = (xy - lo) / scale
    out += (1.0 - span / scale) / 2.0
    return out


def _mood_labels(X: np.ndarray, ids: np.ndarray, xy: np.ndarray) -> list[dict]:
    """Place each mood at the densest spot among its best-matching tracks."""
    if X.shape[0] < 8:
        return []
    try:
        from musik.embed.clap import embed_texts

        T = np.stack(embed_texts([prompt for *_rest, prompt in MOODS]))
    except Exception as exc:  # noqa: BLE001
        log.warning("library map: CLAP text labels unavailable: %s", exc)
        return []
    if T.shape[1] != X.shape[1]:
        log.warning("library map: text dim %s != audio dim %s", T.shape[1], X.shape[1])
        return []
    sims = X @ T.T
    # CLAP text/audio cosines sit in a narrow band and differ by prompt, so
    # rank tracks per mood on a standardized score.
    z = (sims - sims.mean(axis=0)) / np.maximum(sims.std(axis=0), 1e-6)
    n = X.shape[0]
    top_k = int(np.clip(round(n * 0.03), 8, 80))
    labels = []
    for j, (mood_id, name, color, _prompt) in enumerate(MOODS):
        order = np.argsort(-z[:, j])
        pts = xy[order[:top_k]]
        # The best matches can sit on two islands; a plain mean would land
        # between them. Anchor on the point with the most close neighbours.
        dist = np.linalg.norm(pts[:, None, :] - pts[None, :, :], axis=2)
        seed = int(np.argmax((dist < 0.08).sum(axis=1)))
        near = pts[dist[seed] < 0.12]
        x, y = near.mean(axis=0)
        labels.append(
            {
                "id": mood_id,
                "name": name,
                "color": color,
                "x": round(float(x), 4),
                "y": round(float(y), 4),
                "track_ids": [int(t) for t in ids[order[: min(LABEL_MIX_SIZE, n)]]],
            }
        )
    return labels


def build_library_map(*, seed: int = 42) -> MapResult:
    ids, X = _load_vectors()
    if ids.size:
        xy, method = _project(X, seed=seed)
        xy = _normalize(xy)
    else:
        xy, method = np.zeros((0, 2), dtype=np.float32), "none"
    genres, picks = _track_genres(ids)
    labels = _mood_labels(X, ids, xy)
    doc = {
        "version": MAP_VERSION,
        "method": method,
        "updated_at": utcnow(),
        "genres": genres,
        "points": [
            [int(tid), round(float(x), 4), round(float(y), 4), int(g)]
            for tid, (x, y), g in zip(ids.tolist(), xy.tolist(), picks, strict=True)
        ],
        "labels": labels,
    }
    with connect() as conn:
        conn.execute(
            """
            INSERT INTO scan_state(key, value) VALUES(:key, :value)
            ON CONFLICT(key) DO UPDATE SET value = excluded.value
            """,
            {"key": MAP_KEY, "value": json.dumps(doc, ensure_ascii=False, separators=(",", ":"))},
        )
    log.info("library map: %d points via %s, %d labels", ids.size, method, len(labels))
    return MapResult(n=int(ids.size), method=method, labels=len(labels), genres=len(genres))
