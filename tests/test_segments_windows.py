"""Windowing must cover the author's three spans with model-sized windows."""

from __future__ import annotations

import numpy as np
import soundfile as sf

from musik.embed.segments import (
    DEFAULT_SEGMENT_SEC,
    DEFAULT_SPAN_SEC,
    SEGMENT_STRATEGY,
    load_segment_audio,
    plan_spans,
    plan_windows,
)


def _write_tone(path, *, seconds, sr=48000):
    t = np.linspace(0.0, seconds, int(sr * seconds), endpoint=False)
    sf.write(str(path), (0.2 * np.sin(2 * np.pi * 220.0 * t)).astype(np.float32), sr)
    return path


def test_strategy_id_tracks_the_windowing():
    # The cache key embeds the strategy id; a windowing change must rename it.
    assert SEGMENT_STRATEGY == "clap_3x30_v2"
    assert (DEFAULT_SPAN_SEC, DEFAULT_SEGMENT_SEC) == (30.0, 10.0)


def test_three_spans_are_unchanged():
    spans = plan_spans(240.0)

    assert [name for name, _off in spans] == ["start", "middle", "end"]
    assert [off for _n, off in spans] == [0.0, 105.0, 210.0]


def test_each_span_is_covered_by_model_sized_windows():
    windows = plan_windows(240.0)

    assert len(windows) == 9
    assert [w.offset_sec for w in windows] == [0, 10, 20, 105, 115, 125, 210, 220, 230]
    assert {w.duration_sec for w in windows} == {10.0}


def test_windows_are_exactly_one_model_input(tmp_path):
    wav = _write_tone(tmp_path / "long.wav", seconds=240.0)

    segments = load_segment_audio(wav, sample_rate=48000, segment_sec=10.0)

    # Exactly 10s each: nothing for the extractor to crop at random.
    assert {y.size for _w, y in segments} == {480_000}
    assert len(segments) == 9


def test_span_leftover_is_not_dropped():
    # A 25s track is one span; the last 5s are covered by a flush-ending window.
    windows = plan_windows(25.0)

    assert [w.name for w in windows] == ["full", "full+10s", "full+tail"]
    assert windows[-1].offset_sec == 15.0


def test_track_shorter_than_one_window(tmp_path):
    wav = _write_tone(tmp_path / "short.wav", seconds=6.0)

    segments = load_segment_audio(wav, sample_rate=48000, segment_sec=10.0)

    assert [w.name for w, _y in segments] == ["full"]
    assert segments[0][1].size <= 480_000
