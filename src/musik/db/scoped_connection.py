from __future__ import annotations

from contextlib import contextmanager
from contextvars import ContextVar
from typing import Iterator

active_profile: ContextVar[str | None] = ContextVar("musik_profile", default=None)


@contextmanager
def profile_scope(profile_id: str) -> Iterator[None]:
    token = active_profile.set(profile_id)
    try:
        yield
    finally:
        active_profile.reset(token)
