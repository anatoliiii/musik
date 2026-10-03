from __future__ import annotations

import logging
import shutil
import subprocess
from dataclasses import dataclass
from pathlib import Path

import numpy as np

logger = logging.getLogger(__name__)

ANALYSIS_SR = 22_050
KEYS = ["C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"]


def compute_fingerprint(path: Path) -> str | None:
    """Chromaprint fingerprint via fpcalc (system) or pyacoustid."""
    try:
        import acoustid  # type: ignore

        duration, fp = acoustid.fingerprint_file(str(path))
        _ = duration
        if isinstance(fp, bytes):
            return fp.decode("ascii", errors="ignore")
        return str(fp) if fp else None
    except Exception:
        pass

    try:
        proc = subprocess.run(
            ["fpcalc", "-raw", str(path)],
            capture_output=True,
            text=True,
            timeout=120,
            check=False,
        )
        if proc.returncode == 0:
            for line in proc.stdout.splitlines():
                if line.startswith("FINGERPRINT="):
                    return line.split("=", 1)[1].strip() or None
    except FileNotFoundError:
        logger.debug("fpcalc not installed — fingerprint skipped")
    except Exception as exc:
        logger.warning("fingerprint failed for %s: %s", path, exc)
    return None


@dataclass(frozen=True)
class AudioParams:
    """LUFS / BPM / key / mode from a single decode of the file."""

    lufs: float | None = None
    bpm: float | None = None
    key: str | None = None
    mode: str | None = None


def _ffprobe_sample_rate(path: Path) -> int | None:
    ffprobe = shutil.which("ffprobe")
    if not ffprobe:
        return None
    try:
        out = subprocess.check_output(
            [
                ffprobe,
                "-v", "error",
                "-select_streams", "a:0",
                "-show_entries", "stream=sample_rate",
                "-of", "default=noprint_wrappers=1:nokey=1",
                str(path),
            ],
            timeout=30,
            # see _ffprobe_duration: a warning on stderr must not reach the value
            stderr=subprocess.DEVNULL,
        )
        lines = [line.strip() for line in out.decode(errors="replace").splitlines()]
        return next((int(line) for line in reversed(lines) if line), None)
    except Exception:
        return None


def _ffmpeg_load_mono(path: Path, max_seconds: float) -> tuple[np.ndarray, int] | None:
    """Decode with ffmpeg, keeping the file's own sample rate."""
    ffmpeg = shutil.which("ffmpeg")
    if not ffmpeg:
        return None
    sample_rate = _ffprobe_sample_rate(path) or 48_000
    raw = subprocess.check_output(
        [
            ffmpeg, "-nostdin", "-v", "error",
            "-t", f"{max(max_seconds, 0.05):.3f}",
            "-i", str(path),
            "-f", "f32le", "-acodec", "pcm_f32le",
            "-ac", "1", "-ar", str(sample_rate),
            "pipe:1",
        ],
        timeout=300,
    )
    audio = np.frombuffer(raw, dtype=np.float32)
    if audio.size == 0:
        return None
    return audio.astype(np.float64), sample_rate


def _load_mono(path: Path, max_seconds: float) -> tuple[np.ndarray, int] | None:
    """
    Decode up to max_seconds of mono audio at the file's own sample rate.

    soundfile → librosa → ffmpeg. The last step is what the embedding path has
    had all along (segments._ffmpeg_load_window); without it libsndfile's lack of
    AAC support silently left every .m4a track with no loudness, tempo or key,
    so the ranker saw bpm_missing / lufs_missing for a whole format.
    """
    import librosa
    import soundfile as sf

    try:
        info = sf.info(str(path))
        sr = int(info.samplerate)
        frames = int(min(info.frames, max_seconds * sr))
        audio, _ = sf.read(str(path), frames=frames, dtype="float64", always_2d=True)
        return np.mean(audio, axis=1), sr
    except Exception:
        pass

    try:
        audio, sr = librosa.load(str(path), sr=None, mono=True, duration=max_seconds)
        return audio.astype(np.float64), int(sr)
    except Exception:
        logger.info("soundfile/librosa cannot decode %s — trying ffmpeg", path)

    try:
        return _ffmpeg_load_mono(path, max_seconds)
    except Exception as exc:
        logger.warning("decode failed for %s: %s", path, exc)
        return None


def _lufs_from(audio: np.ndarray, sr: int) -> float | None:
    import pyloudnorm as pyln

    if audio.size < sr * 0.5:
        return None
    loudness = float(pyln.Meter(sr).integrated_loudness(audio))
    if np.isnan(loudness) or np.isinf(loudness):
        return None
    return loudness


def _bpm_from(y22: np.ndarray) -> float | None:
    import librosa

    tempo, _ = librosa.beat.beat_track(y=y22, sr=ANALYSIS_SR)
    if hasattr(tempo, "__len__"):
        tempo = float(np.asarray(tempo).ravel()[0])
    return float(tempo)


def _key_mode_from(y22: np.ndarray) -> tuple[str | None, str | None]:
    import librosa

    chroma_mean = librosa.feature.chroma_cqt(y=y22, sr=ANALYSIS_SR).mean(axis=1)
    idx = int(np.argmax(chroma_mean))
    major = chroma_mean[idx] + chroma_mean[(idx + 4) % 12] + chroma_mean[(idx + 7) % 12]
    minor = chroma_mean[idx] + chroma_mean[(idx + 3) % 12] + chroma_mean[(idx + 7) % 12]
    return KEYS[idx], "major" if major >= minor else "minor"


def compute_audio_params(
    path: Path,
    *,
    max_seconds: float = 60.0,
    key_seconds: float = 45.0,
) -> AudioParams:
    """
    LUFS, BPM and key/mode from ONE decode.

    compute_lufs / compute_bpm / compute_key_mode each decoded the same file
    separately (and resampled it twice), which is three passes over every track
    during a scan. The analysis windows are unchanged, so the values are the
    same — see tests/test_audio_params.py.
    """
    import librosa

    loaded = _load_mono(path, max_seconds)
    if loaded is None:
        return AudioParams()
    mono64, sr = loaded

    lufs = None
    try:
        lufs = _lufs_from(mono64, sr)
    except Exception as exc:
        logger.warning("LUFS failed for %s: %s", path, exc)

    bpm = key = mode = None
    try:
        y22 = librosa.resample(
            mono64.astype(np.float32), orig_sr=sr, target_sr=ANALYSIS_SR, res_type="soxr_hq"
        )
    except Exception as exc:
        logger.warning("resample failed for %s: %s", path, exc)
        return AudioParams(lufs=lufs)

    try:
        bpm = _bpm_from(y22[: int(ANALYSIS_SR * max_seconds)])
    except Exception as exc:
        logger.warning("BPM failed for %s: %s", path, exc)
    try:
        key, mode = _key_mode_from(y22[: int(ANALYSIS_SR * key_seconds)])
    except Exception as exc:
        logger.warning("key extract failed for %s: %s", path, exc)

    return AudioParams(lufs=lufs, bpm=bpm, key=key, mode=mode)


def compute_lufs(path: Path, max_seconds: float = 60.0) -> float | None:
    """EBU R128 integrated loudness via pyloudnorm."""
    try:
        loaded = _load_mono(path, max_seconds)
        if loaded is None:
            return None
        return _lufs_from(*loaded)
    except Exception as exc:
        logger.warning("LUFS failed for %s: %s", path, exc)
        return None


def compute_bpm(path: Path, max_seconds: float = 60.0) -> float | None:
    """BPM via librosa beat_track (madmom optional later)."""
    try:
        import librosa

        y, _sr = librosa.load(str(path), sr=ANALYSIS_SR, mono=True, duration=max_seconds)
        return _bpm_from(y)
    except Exception as exc:
        logger.warning("BPM failed for %s: %s", path, exc)
        return None


def compute_key_mode(path: Path, max_seconds: float = 45.0) -> tuple[str | None, str | None]:
    """
    Key/mode extraction.
    Prefers essentia if installed; otherwise chroma-based heuristic via librosa.
    """
    try:
        import essentia.standard as es  # type: ignore

        audio = es.MonoLoader(filename=str(path), sampleRate=44100)()
        max_samples = int(44100 * max_seconds)
        if audio.shape[0] > max_samples:
            audio = audio[:max_samples]
        key, scale, _strength = es.KeyExtractor()(audio)
        return str(key), str(scale)
    except Exception:
        pass

    try:
        import librosa

        y, _sr = librosa.load(str(path), sr=ANALYSIS_SR, mono=True, duration=max_seconds)
        return _key_mode_from(y)
    except Exception as exc:
        logger.warning("key extract failed for %s: %s", path, exc)
        return None, None
