from __future__ import annotations

from pathlib import Path
from urllib.parse import unquote, urlsplit


def resolve_sqlite_path(
    database_url: str | None, legacy_path: Path | None, default_path: Path
) -> Path:
    """Resolve the current SQLite backend without silently ignoring a URL."""
    if not database_url:
        return legacy_path or default_path
    if legacy_path is not None:
        raise ValueError("MUSIK_DATABASE_URL and MUSIK_DB_PATH cannot both be set")
    parsed = urlsplit(database_url)
    if parsed.scheme == "sqlite":
        if parsed.netloc or parsed.query or parsed.fragment or not parsed.path.startswith("/"):
            raise ValueError("MUSIK_DATABASE_URL must be an absolute sqlite:/// path without options")
        return Path(unquote(parsed.path))
    if parsed.scheme in ("postgres", "postgresql"):
        raise ValueError("PostgreSQL storage adapter is not available yet")
    raise ValueError("unsupported MUSIK_DATABASE_URL scheme")
