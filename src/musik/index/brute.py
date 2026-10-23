"""In-memory embedding matrix + brute-force cosine neighbors."""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from musik.db.schema import connect


@dataclass(frozen=True)
class Neighbor:
    track_id: int
    artist: str
    title: str
    cosine: float
    path: str = ""


@dataclass
class EmbeddingIndex:
    """Brute-force index: all unit vectors in a dense matrix."""

    track_ids: np.ndarray  # (N,) int64
    matrix: np.ndarray  # (N, D) float32, L2-normalized
    meta: list[dict]  # parallel to rows: id, artist, title, path, file_md5
    md5s: list[str]

    @property
    def size(self) -> int:
        return int(self.matrix.shape[0])

    @property
    def dim(self) -> int:
        return int(self.matrix.shape[1]) if self.size else 0

    def row_of(self, track_id: int) -> int | None:
        hits = np.where(self.track_ids == track_id)[0]
        if hits.size == 0:
            return None
        return int(hits[0])

    def neighbors(
        self,
        track_id: int,
        *,
        k: int = 10,
        exclude_same_md5: bool = True,
    ) -> list[Neighbor]:
        idx = self.row_of(track_id)
        if idx is None:
            raise KeyError(f"track_id={track_id} not in index (need ready embedding)")
        sims = self.matrix @ self.matrix[idx]
        sims = sims.copy()
        sims[idx] = -2.0
        if exclude_same_md5:
            my_md5 = self.md5s[idx]
            for j, md5 in enumerate(self.md5s):
                if j != idx and md5 and md5 == my_md5:
                    sims[j] = -2.0
        k = max(0, min(k, self.size - 1))
        if k == 0:
            return []
        # partial top-k
        top = np.argpartition(-sims, kth=min(k, sims.size - 1))[:k]
        top = top[np.argsort(-sims[top])]
        out: list[Neighbor] = []
        for j in top:
            if sims[j] <= -1.5:
                continue
            m = self.meta[int(j)]
            out.append(
                Neighbor(
                    track_id=int(m["id"]),
                    artist=m.get("artist") or "",
                    title=m.get("title") or "",
                    cosine=float(sims[j]),
                    path=m.get("path") or "",
                )
            )
        return out

    def sims_to_vector(self, vec: np.ndarray) -> np.ndarray:
        v = np.asarray(vec, dtype=np.float32).reshape(-1)
        n = float(np.linalg.norm(v))
        if n > 1e-12:
            v = v / n
        if v.size != self.dim:
            padded = np.zeros(self.dim, dtype=np.float32)
            padded[: min(v.size, self.dim)] = v[: self.dim]
            v = padded
            n = float(np.linalg.norm(v))
            if n > 1e-12:
                v = v / n
        return self.matrix @ v

    def centroid(self, indices: list[int] | None = None) -> np.ndarray:
        if self.size == 0:
            return np.zeros(0, dtype=np.float32)
        if indices is None:
            mean = self.matrix.mean(axis=0)
        else:
            mean = self.matrix[np.asarray(indices, dtype=np.int64)].mean(axis=0)
        n = float(np.linalg.norm(mean))
        return (mean / n).astype(np.float32) if n > 1e-12 else mean.astype(np.float32)

def load_index() -> EmbeddingIndex:
    """Load all active ready embeddings into RAM."""
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT t.id, t.title, t.artist, t.album, t.path, t.file_md5,
                   t.duration, t.created_at,
                   f.embedding, f.embedding_dim, f.cluster_id, f.bpm,
                   COALESCE(f.lufs, t.lufs) AS lufs, f.key_name,
                   COALESCE(ts.plays, 0) AS plays,
                   COALESCE(ts.finishes, 0) AS finishes,
                   COALESCE(ts.partial, 0) AS partial,
                   COALESCE(ts.early_skips, 0) AS early_skips,
                   COALESCE(ts.likes, 0) AS likes,
                   COALESCE(ts.dislikes, 0) AS dislikes,
                   ts.last_played_at, ts.last_finished_at,
                   COALESCE(rs.shown, 0) AS shown,
                   COALESCE(rs.skipped_early, 0) AS skip_early,
                   COALESCE(rs.completed, 0) AS completed
            FROM tracks t
            JOIN features f ON f.track_id = t.id
            LEFT JOIN track_stats ts
              ON ts.track_id = t.id AND ts.profile_id = :profile
            LEFT JOIN rec_stats rs
              ON rs.track_id = t.id AND rs.profile_id = :profile
            WHERE t.is_active = 1
              AND t.is_duplicate_of IS NULL
              AND f.status = 'ready'
              AND f.embedding IS NOT NULL
            ORDER BY t.id
            """,
            {"profile": conn.profile_id},
        ).fetchall()

    if not rows:
        return EmbeddingIndex(
            track_ids=np.zeros(0, dtype=np.int64),
            matrix=np.zeros((0, 0), dtype=np.float32),
            meta=[],
            md5s=[],
        )

    meta: list[dict] = []
    md5s: list[str] = []
    vectors: list[np.ndarray] = []
    for r in rows:
        dim = int(r["embedding_dim"] or 0)
        vec = np.frombuffer(r["embedding"], dtype=np.float32)
        if dim and vec.size != dim:
            vec = vec[:dim] if vec.size > dim else vec
        vec = np.asarray(vec, dtype=np.float32).reshape(-1)
        n = float(np.linalg.norm(vec))
        if n > 1e-12:
            vec = vec / n
        vectors.append(vec)
        meta.append(
            {
                "id": int(r["id"]),
                "title": r["title"],
                "artist": r["artist"],
                "album": r["album"],
                "path": r["path"],
                "file_md5": r["file_md5"],
                "duration": r["duration"],
                "created_at": r["created_at"],
                "cluster_id": r["cluster_id"],
                "bpm": r["bpm"],
                "lufs": r["lufs"],
                "key": r["key_name"],
                "plays": int(r["plays"] or 0),
                "finishes": int(r["finishes"] or 0),
                "partial": int(r["partial"] or 0),
                "early_skips": int(r["early_skips"] or 0),
                "likes": int(r["likes"] or 0),
                "dislikes": int(r["dislikes"] or 0),
                "last_played_at": r["last_played_at"],
                "last_finished_at": r["last_finished_at"],
                "shown": int(r["shown"] or 0),
                "skip_early": int(r["skip_early"] or 0),
                "completed": int(r["completed"] or 0),
            }
        )
        md5s.append(r["file_md5"] or "")

    # pad/truncate to common dim
    dim = max(v.size for v in vectors)
    mat = np.zeros((len(vectors), dim), dtype=np.float32)
    for i, v in enumerate(vectors):
        mat[i, : v.size] = v
        n = float(np.linalg.norm(mat[i]))
        if n > 1e-12:
            mat[i] /= n

    ids = np.asarray([m["id"] for m in meta], dtype=np.int64)
    return EmbeddingIndex(track_ids=ids, matrix=mat, meta=meta, md5s=md5s)


def find_tracks(query: str, *, limit: int = 20) -> list[dict]:
    """Simple LIKE search by artist/title for CLI."""
    q = f"%{query.strip()}%"
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT t.id, t.artist, t.title, t.path, f.status
            FROM tracks t
            LEFT JOIN features f ON f.track_id = t.id
            WHERE t.is_active = 1 AND t.is_duplicate_of IS NULL
              AND (lower(t.title) LIKE lower(:q) OR lower(t.artist) LIKE lower(:q)
                   OR lower(t.path) LIKE lower(:q))
            ORDER BY t.artist, t.title
            LIMIT :limit
            """,
            {"q": q, "limit": limit},
        ).fetchall()
        return [dict(r) for r in rows]


def get_track(track_id: int) -> dict | None:
    with connect() as conn:
        row = conn.execute(
            """
            SELECT t.id, t.artist, t.title, t.path, t.file_md5, f.status, f.cluster_id
            FROM tracks t
            LEFT JOIN features f ON f.track_id = t.id
            WHERE t.id = :track_id
            """,
            {"track_id": track_id},
        ).fetchone()
        return dict(row) if row else None
