# Golden fixtures (ranker parity)

- `feature_order_v3.txt` — one feature name per line; must match `player/internal/queue/ranker_spec.json` (`schema_version: 3`) and Python `FEATURE_ORDER`.
- `ranker_fixture.json` — shared ranking scenario for Go/Python parity tests.

Older schema snapshots are not kept here; training rejects `schema_version` below the current contract.
