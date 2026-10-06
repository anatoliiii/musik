"""Shared test fixtures.

`test_database` selects the backend for repository tests. SQLite is the
default; MUSIK_TEST_BACKEND=postgres runs the same test against a fresh
PostgreSQL database cloned from the migrated template MUSIK_TEST_POSTGRES_TEMPLATE
(default musik_template) on the server in MUSIK_TEST_POSTGRES_URL.
"""
from __future__ import annotations

import os
import uuid
from collections.abc import Callable, Iterator
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit

import pytest

from musik.config import get_settings

BACKEND = os.environ.get("MUSIK_TEST_BACKEND", "sqlite")


def _with_database(url: str, name: str) -> str:
    parts = urlsplit(url)
    return urlunsplit((parts.scheme, parts.netloc, "/" + name, parts.query, parts.fragment))


@pytest.fixture
def test_database(monkeypatch: pytest.MonkeyPatch) -> Iterator[Callable[[Path], str | None]]:
    """Return use(sqlite_path) -> database URL (None for SQLite)."""
    created: list[str] = []
    server = os.environ.get("MUSIK_TEST_POSTGRES_URL", "")

    def use(sqlite_path: Path) -> str | None:
        if BACKEND != "postgres":
            monkeypatch.delenv("MUSIK_DATABASE_URL", raising=False)
            monkeypatch.setenv("MUSIK_DB_PATH", str(sqlite_path))
            get_settings.cache_clear()
            return None
        if not server:
            pytest.fail("MUSIK_TEST_BACKEND=postgres requires MUSIK_TEST_POSTGRES_URL")
        import psycopg

        template = os.environ.get("MUSIK_TEST_POSTGRES_TEMPLATE", "musik_template")
        name = "musik_py_" + uuid.uuid4().hex[:16]
        with psycopg.connect(server, autocommit=True) as admin:
            admin.execute("SELECT pg_advisory_lock(727001)")
            try:
                admin.execute(f'CREATE DATABASE {name} TEMPLATE "{template}"')
            finally:
                admin.execute("SELECT pg_advisory_unlock(727001)")
        created.append(name)
        url = _with_database(server, name)
        monkeypatch.delenv("MUSIK_DB_PATH", raising=False)
        monkeypatch.setenv("MUSIK_DATABASE_URL", url)
        get_settings.cache_clear()
        return url

    yield use

    get_settings.cache_clear()
    if created:
        import psycopg

        from musik.db import orm

        for url, engine in list(orm._ENGINES.items()):
            if any(url.endswith("/" + name) for name in created):
                engine.dispose()
                del orm._ENGINES[url]
        with psycopg.connect(server, autocommit=True) as admin:
            for name in created:
                admin.execute(f"DROP DATABASE IF EXISTS {name} WITH (FORCE)")


@pytest.fixture
def empty_postgres_database() -> Iterator[str]:
    """An empty PostgreSQL database (no schema) for migration and transfer tests."""
    server = os.environ.get("MUSIK_TEST_POSTGRES_URL", "")
    if not server:
        pytest.skip("requires a disposable PostgreSQL test database")
    import psycopg

    name = "musik_empty_" + uuid.uuid4().hex[:16]
    with psycopg.connect(server, autocommit=True) as admin:
        admin.execute(f"CREATE DATABASE {name}")
    try:
        yield _with_database(server, name)
    finally:
        from musik.db import orm

        for url, engine in list(orm._ENGINES.items()):
            if url.endswith("/" + name):
                engine.dispose()
                del orm._ENGINES[url]
        with psycopg.connect(server, autocommit=True) as admin:
            admin.execute(f"DROP DATABASE IF EXISTS {name} WITH (FORCE)")
