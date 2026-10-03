"""ffprobe warnings must not reach the parsed value."""

from __future__ import annotations

import subprocess

import pytest

from musik.embed import segments
from musik.scanner import audio_params

# What ffprobe prints for a file with a damaged frame header: the warning goes to
# stderr, the duration to stdout. Merging the two streams used to break parsing.
NOISY = b"[mp3float @ 0x557e3e9eea40] Header missing\n212.248200\n"


def test_parse_ffprobe_number_takes_the_value_not_the_warning():
    assert segments._parse_ffprobe_number(NOISY) == pytest.approx(212.2482)


def test_parse_ffprobe_number_tolerates_trailing_blank_lines():
    assert segments._parse_ffprobe_number(b"5.5\n\n\n") == pytest.approx(5.5)


def test_parse_ffprobe_number_without_value():
    with pytest.raises(ValueError):
        segments._parse_ffprobe_number(b"\n\n")


def test_ffprobe_duration_does_not_merge_stderr(monkeypatch, tmp_path):
    seen = {}

    def fake_check_output(cmd, **kwargs):
        seen.update(kwargs)
        return NOISY

    monkeypatch.setattr(segments.shutil, "which", lambda _name: "/usr/bin/ffprobe")
    monkeypatch.setattr(segments.subprocess, "check_output", fake_check_output)

    assert segments._ffprobe_duration(tmp_path / "broken.mp3") == pytest.approx(212.2482)
    assert seen["stderr"] is subprocess.DEVNULL


def test_ffprobe_sample_rate_ignores_warnings(monkeypatch, tmp_path):
    monkeypatch.setattr(audio_params.shutil, "which", lambda _name: "/usr/bin/ffprobe")
    monkeypatch.setattr(
        audio_params.subprocess,
        "check_output",
        lambda cmd, **kwargs: b"[mp3float @ 0x1] Header missing\n44100\n",
    )

    assert audio_params._ffprobe_sample_rate(tmp_path / "broken.mp3") == 44100
