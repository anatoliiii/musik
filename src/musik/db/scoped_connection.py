from __future__ import annotations

import sqlite3
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


def bind_profile(sql: str, parameters, profile_id: str):
    if ":musik_profile" not in sql:
        return sql, parameters
    out, i, index = [], 0, 0
    while i < len(sql):
        start = i
        if sql[i] in "'\"`[":
            end = "]" if sql[i] == "[" else sql[i]
            i += 1
            while i < len(sql):
                if sql[i] == end:
                    i += 1
                    if i < len(sql) and sql[i] == end:
                        i += 1
                        continue
                    break
                i += 1
            out.append(sql[start:i])
            continue
        if sql[i:i + 2] == "--":
            i = sql.find("\n", i)
            if i == -1:
                i = len(sql)
            out.append(sql[start:i])
            continue
        if sql[i:i + 2] == "/*":
            end = sql.find("*/", i + 2)
            i = len(sql) if end == -1 else end + 2
            out.append(sql[start:i])
            continue
        if sql[i] == "?":
            out.append(f":arg{index}")
            index += 1
        else:
            out.append(sql[i])
        i += 1
    values = dict(parameters) if isinstance(parameters, dict) else {f"arg{n}": value for n, value in enumerate(parameters)}
    values["musik_profile"] = profile_id
    return "".join(out), values


class ProfileConnection(sqlite3.Connection):
    profile_id: str = ""

    def execute(self, sql, parameters=()):
        sql, parameters = bind_profile(sql, parameters, self.profile_id)
        return super().execute(sql, parameters)

    def executemany(self, sql, parameters):
        if ":musik_profile" not in sql:
            return super().executemany(sql, parameters)
        bound_sql, _ = bind_profile(sql, (), self.profile_id)
        return super().executemany(bound_sql, (bind_profile(sql, row, self.profile_id)[1] for row in parameters))
