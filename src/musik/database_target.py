from __future__ import annotations

from pathlib import Path
from urllib.parse import unquote, urlsplit


def normalize_database_url(database_url: str) -> str:
    """Validate and normalize the public URL without revealing its credentials."""
    parsed = urlsplit(database_url)
    scheme = parsed.scheme.lower()
    if scheme == "sqlite":
        if parsed.netloc or parsed.query or parsed.fragment or not parsed.path.startswith("/"):
            raise ValueError("MUSIK_DATABASE_URL must be an absolute sqlite:/// path without options")
        return database_url
    if scheme in ("postgres", "postgresql"):
        if not parsed.hostname or not parsed.path.strip("/"):
            raise ValueError("PostgreSQL MUSIK_DATABASE_URL must include a host and database")
        try:
            _ = parsed.port
        except ValueError as exc:
            raise ValueError("invalid PostgreSQL port in MUSIK_DATABASE_URL") from exc
        return "postgresql://" + database_url.split("://", 1)[1]
    raise ValueError("unsupported MUSIK_DATABASE_URL scheme")


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
        raise ValueError("PostgreSQL URL cannot be resolved to a SQLite filesystem path")
    raise ValueError("unsupported MUSIK_DATABASE_URL scheme")
