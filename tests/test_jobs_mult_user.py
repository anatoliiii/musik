from __future__ import annotations

from pathlib import Path
from uuid import uuid4

from musik.config import get_settings
from musik.db.schema import connect, init_db
from musik.db.scoped_connection import active_profile
from musik.jobs import runner


def _add_active_profiles(db_path: Path) -> list[str]:
    now = "2026-10-04T00:00:00+00:00"
    profiles = [str(uuid4()), str(uuid4())]
    with connect() as conn:
        for index, profile_id in enumerate(profiles):
            user_id = str(uuid4())
            conn.execute(
                "INSERT INTO users(id,status,display_name,created_at,updated_at) "
                "VALUES (:id, 'active', :name, :now, :now)",
                {"id": user_id, "name": f"User {index}", "now": now},
            )
            conn.execute(
                "INSERT INTO user_roles(user_id,role) VALUES (:id, 'user')", {"id": user_id}
            )
            conn.execute(
                "INSERT INTO profiles(id,owner_user_id,name,is_default,created_at,updated_at) "
                "VALUES (:id, :owner, :name, 1, :now, :now)",
                {"id": profile_id, "owner": user_id, "name": f"Profile {index}", "now": now},
            )
    return profiles


def test_process_job_uses_explicit_profile_in_multi_user_mode(
    monkeypatch, tmp_path: Path, test_database
) -> None:
    db_path = tmp_path / "worker-explicit-profile.sqlite"
    test_database(db_path)
    monkeypatch.setenv("MUSIK_MULTI_USER", "true")
    get_settings.cache_clear()
    try:
        init_db()
        first, second = _add_active_profiles(db_path)
        visited: list[str | None] = []
        monkeypatch.setattr(
            runner,
            "_process_job",
            lambda _job: visited.append(active_profile.get()) or {"ok": True},
        )

        result = runner.process_job(
            {"kind": "mix_pack", "payload": {"profile_id": second}}
        )

        assert result == {"ok": True}
        assert visited == [second]
        assert first not in visited
    finally:
        get_settings.cache_clear()


def test_process_job_runs_unselected_profile_jobs_for_each_active_profile(
    monkeypatch, tmp_path: Path, test_database
) -> None:
    db_path = tmp_path / "worker-all-profiles.sqlite"
    test_database(db_path)
    monkeypatch.setenv("MUSIK_MULTI_USER", "true")
    get_settings.cache_clear()
    try:
        init_db()
        profiles = _add_active_profiles(db_path)
        visited: list[str | None] = []
        monkeypatch.setattr(
            runner,
            "_process_job",
            lambda _job: visited.append(active_profile.get()) or {"ok": True},
        )

        result = runner.process_job({"kind": "mix_pack", "payload": {}})

        assert result == {"profiles": {profile_id: {"ok": True} for profile_id in profiles}}
        assert set(visited) == set(profiles)
    finally:
        get_settings.cache_clear()
