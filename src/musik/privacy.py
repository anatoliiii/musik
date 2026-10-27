"""Which profiles may see a library track.

A track under ``<library>/_private/<profile id>/`` belongs to that profile
alone; every other track is shared. The Go player applies the same rule
(player/internal/privacy), so keep the two in step.
"""

from __future__ import annotations

PRIVATE_DIR = "_private"


def private_owner(path: str | None) -> str:
    """Return the profile that owns ``path``, or "" for a shared track."""
    parts = str(path or "").replace("\\", "/").split("/")
    for i in range(len(parts) - 1):
        if parts[i] == PRIVATE_DIR and parts[i + 1]:
            return parts[i + 1]
    return ""


def visible_to(path: str | None, profile_id: str | None) -> bool:
    owner = private_owner(path)
    return owner == "" or owner == (profile_id or "")
