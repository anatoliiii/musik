from pathlib import Path

import pytest

from musik.database_target import resolve_sqlite_path


def test_sqlite_url_and_legacy_path() -> None:
    default = Path("/default/musik.db")
    assert resolve_sqlite_path(None, None, default) == default
    assert resolve_sqlite_path(None, Path("/legacy.db"), default) == Path("/legacy.db")
    assert resolve_sqlite_path("sqlite:///data/music%20db.sqlite", None, default) == Path(
        "/data/music db.sqlite"
    )


@pytest.mark.parametrize(
    "url,legacy",
    [
        ("sqlite:///new.db", Path("/old.db")),
        ("sqlite://relative/db", None),
        ("sqlite:///new.db?mode=ro", None),
        ("postgresql://user:secret@host/db", None),
    ],
)
def test_invalid_or_unavailable_targets_fail_closed(url: str, legacy: Path | None) -> None:
    with pytest.raises(ValueError):
        resolve_sqlite_path(url, legacy, Path("/default.db"))
