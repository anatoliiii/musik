"""Embedding the same audio twice must give the same vector.

Needs the CLAP weights, so it skips when they are not in the local HF cache.
Run it where the model is available:  pytest tests/test_embed_determinism.py
"""

from __future__ import annotations

import numpy as np
import pytest
import soundfile as sf

pytest.importorskip("torch")
pytest.importorskip("transformers")


@pytest.fixture(scope="module")
def clap():
    from musik.embed.clap import DEFAULT_CLAP_MODEL, _load_model

    try:
        _load_model(DEFAULT_CLAP_MODEL)
    except Exception as exc:  # weights absent / offline
        pytest.skip(f"CLAP weights unavailable: {exc}")
    return DEFAULT_CLAP_MODEL


def _write_tone(path, *, seconds, sr=48000):
    t = np.linspace(0.0, seconds, int(sr * seconds), endpoint=False)
    y = 0.2 * np.sin(2 * np.pi * 220.0 * t) + 0.1 * np.sin(2 * np.pi * 523.0 * t)
    sf.write(str(path), y.astype(np.float32), sr)
    return path


def test_same_file_twice_is_identical(tmp_path, clap):
    from musik.embed.clap import embed_file

    wav = _write_tone(tmp_path / "tone.wav", seconds=75.0)

    first = embed_file(wav, model_id=clap)
    second = embed_file(wav, model_id=clap)

    # Windows are exactly one extractor input, so rand_trunc never fires.
    assert float(np.dot(first, second)) == pytest.approx(1.0, abs=1e-6)
    assert np.max(np.abs(first - second)) == pytest.approx(0.0, abs=1e-6)


def test_batch_matches_window_by_window(tmp_path, clap):
    from musik.embed.clap import embed_waveform, embed_waveforms
    from musik.embed.segments import DEFAULT_SR, load_segment_audio

    wav = _write_tone(tmp_path / "tone.wav", seconds=75.0)
    windows = [y for _w, y in load_segment_audio(wav, sample_rate=DEFAULT_SR, segment_sec=10.0)]

    batched = embed_waveforms(windows, model_id=clap)
    one_by_one = [embed_waveform(y, model_id=clap) for y in windows]

    for a, b in zip(batched, one_by_one):
        assert float(np.dot(a, b)) == pytest.approx(1.0, abs=1e-5)


def test_window_longer_than_model_input_is_trimmed(tmp_path, clap):
    from musik.embed.clap import embed_waveforms, model_window_samples

    limit = model_window_samples(clap)
    rng = np.random.default_rng(0)
    long_window = rng.standard_normal(limit * 3).astype(np.float32) * 0.1

    # Same signal, pre-trimmed by the caller vs trimmed inside: one vector.
    inside = embed_waveforms([long_window], model_id=clap)[0]
    outside = embed_waveforms([long_window[:limit]], model_id=clap)[0]

    assert float(np.dot(inside, outside)) == pytest.approx(1.0, abs=1e-6)
