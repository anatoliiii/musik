"""The Go test fixture must be the schema `musik db migrate` produces."""
from __future__ import annotations

import importlib.util
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "go_test_schema.py"


def test_go_fixture_matches_migrations() -> None:
    spec = importlib.util.spec_from_file_location("go_test_schema", SCRIPT)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    assert module.FIXTURE.read_text(encoding="utf-8") == module.generate(), (
        "player/internal/testdb/schema.sql is stale; run "
        "`uv run python scripts/go_test_schema.py --write`"
    )
