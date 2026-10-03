"""Load three fixed windows from a track: start / middle / end."""

from __future__ import annotations

import logging
import shutil
import subprocess
from dataclasses import dataclass
from pathlib import Path

import librosa
import numpy as np

# Strategy id — bump when windowing changes so cache invalidates.
SEGMENT_STRATEGY = "clap_3x30_v2"
# Three listening points per track, 30s each — unchanged.
DEFAULT_SPAN_SEC = 30.0
# A span is fed to the model in chunks of its own input length (max_length_s=10
# for larger_clap_music_and_speech). Handing the model a longer window is not
# "more listening": it crops it to a RANDOM 10s chunk
# (truncation="rand_trunc"), so the other 20s were decoded for nothing and the
# resulting vector differed on every recomputation.
DEFAULT_SEGMENT_SEC = 10.0
DEFAULT_SR = 48_000

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class SegmentWindow:
    name: str
    offset_sec: float
    duration_sec: float


def plan_spans(duration_sec: float, span_sec: float = DEFAULT_SPAN_SEC) -> list[tuple[str, float]]:
    """
    The three listening points: start / middle (centered) / end.

    - short track (<= span): the whole file once
    - drop duplicates when offsets collapse on short songs
    """
    if duration_sec <= 0:
        return []

    if duration_sec <= span_sec + 0.05:
        return [("full", 0.0)]

    candidates = [
        ("start", 0.0),
        ("middle", max(0.0, duration_sec / 2.0 - span_sec / 2.0)),
        ("end", max(0.0, duration_sec - span_sec)),
    ]
    unique: list[tuple[str, float]] = []
    for name, offset in candidates:
        if any(abs(offset - known) < 0.5 for _n, known in unique):
            continue
        unique.append((name, offset))
    return unique


def plan_windows(
    duration_sec: float,
    segment_sec: float = DEFAULT_SEGMENT_SEC,
    span_sec: float = DEFAULT_SPAN_SEC,
) -> list[SegmentWindow]:
    """
    Cover each listening point with model-sized windows.

    A 30s span becomes three consecutive 10s windows, so the whole span really
    reaches the model instead of one randomly cropped chunk of it. Their
    embeddings are averaged afterwards, exactly as three span embeddings were.
    """
    if duration_sec <= 0:
        return []

    windows: list[SegmentWindow] = []
    for name, span_offset in plan_spans(duration_sec, span_sec=span_sec):
        span_len = min(span_sec, max(0.0, duration_sec - span_offset))
        if span_len < segment_sec:
            # Track (or tail) shorter than one model window: take what is there.
            windows.append(SegmentWindow(name, span_offset, span_len))
            continue
        count = int(span_len // segment_sec)
        for index in range(count):
            offset = span_offset + index * segment_sec
            label = name if index == 0 else f"{name}+{int(index * segment_sec)}s"
            windows.append(SegmentWindow(label, offset, segment_sec))
        # Cover a leftover worth listening to by ending flush with the span.
        leftover = span_len - count * segment_sec
        if leftover >= segment_sec / 2.0:
            windows.append(
                SegmentWindow(f"{name}+tail", span_offset + span_len - segment_sec, segment_sec)
            )

    unique: list[SegmentWindow] = []
    for win in windows:
        if any(abs(win.offset_sec - u.offset_sec) < 0.5 for u in unique):
            continue
        unique.append(win)
    return unique


def _ffprobe_duration(path: Path) -> float:
    ffprobe = shutil.which("ffprobe")
    if not ffprobe:
        raise RuntimeError("ffprobe not found")
    out = subprocess.check_output(
        [
            ffprobe,
            "-v", "error",
            "-show_entries", "format=duration",
            "-of", "default=noprint_wrappers=1:nokey=1",
            str(path),
        ],
        timeout=30,
        # NOT merged into stdout: a damaged file makes the decoder print e.g.
        # "[mp3float @ 0x...] Header missing" while still reporting a correct
        # duration, and that line would end up in the value being parsed.
        stderr=subprocess.DEVNULL,
    )
    return _parse_ffprobe_number(out)


def _parse_ffprobe_number(raw: bytes) -> float:
    """Last non-empty line of ffprobe output as a float."""
    lines = [line.strip() for line in raw.decode(errors="replace").splitlines()]
    for line in reversed(lines):
        if line:
            return float(line)
    raise ValueError("ffprobe returned no value")


def audio_duration(path: Path) -> float:
    try:
        return float(librosa.get_duration(path=str(path)))
    except Exception:
        logger.info("soundfile duration failed for %s — trying ffmpeg", path)
        return _ffprobe_duration(path)


def _ffmpeg_load_window(
    path: Path, *, sample_rate: int, offset_sec: float, duration_sec: float
) -> np.ndarray:
    ffmpeg = shutil.which("ffmpeg")
    if not ffmpeg:
        raise RuntimeError("ffmpeg not found")
    cmd = [
        ffmpeg, "-nostdin", "-v", "error",
        "-ss", f"{offset_sec:.3f}",
        "-t", f"{max(duration_sec, 0.05):.3f}",
        "-i", str(path),
        "-f", "f32le", "-acodec", "pcm_f32le",
        "-ac", "1", "-ar", str(int(sample_rate)),
        "pipe:1",
    ]
    raw = subprocess.check_output(cmd, timeout=120)
    y = np.frombuffer(raw, dtype=np.float32)
    if y.size == 0:
        raise RuntimeError("ffmpeg returned empty audio")
    return y.copy()


def _load_window(
    path: Path, win: SegmentWindow, *, sample_rate: int
) -> np.ndarray | None:
    try:
        y, _ = librosa.load(
            str(path),
            sr=sample_rate,
            mono=True,
            offset=win.offset_sec,
            duration=win.duration_sec,
        )
        y = np.asarray(y, dtype=np.float32)
        if y.size:
            return y
    except Exception:
        logger.info("librosa.load failed for %s @ %.1fs — trying ffmpeg", path, win.offset_sec)
    try:
        y = _ffmpeg_load_window(
            path,
            sample_rate=sample_rate,
            offset_sec=win.offset_sec,
            duration_sec=win.duration_sec,
        )
        return y if y.size else None
    except Exception:
        logger.exception("ffmpeg load failed for %s", path)
        return None


def load_segment_audio(
    path: Path,
    *,
    sample_rate: int = DEFAULT_SR,
    segment_sec: float = DEFAULT_SEGMENT_SEC,
    span_sec: float = DEFAULT_SPAN_SEC,
) -> list[tuple[SegmentWindow, np.ndarray]]:
    """Load mono float32 arrays for each planned window."""
    duration = audio_duration(path)
    windows = plan_windows(duration, segment_sec=segment_sec, span_sec=span_sec)
    out: list[tuple[SegmentWindow, np.ndarray]] = []
    max_samples = int(sample_rate * segment_sec)
    for win in windows:
        y = _load_window(path, win, sample_rate=sample_rate)
        if y is None or y.size == 0:
            continue
        # Decoders may hand back a few samples more than asked; keep windows at
        # exactly the requested length so the batch needs no padding and the
        # extractor has nothing to crop.
        if max_samples and y.size > max_samples:
            y = y[:max_samples]
        out.append((win, y))
    if not out:
        raise RuntimeError(f"не удалось декодировать аудио: {path}")
    return out
