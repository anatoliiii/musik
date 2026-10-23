"""The worker builds the player shelf and rejects a kind that is not in it."""

import pytest

from musik.brain.mixes import ensure_mix_generator, generate_mix_pack, load_mix_contract


def test_player_contract_lists_each_shelf_once():
    items = load_mix_contract()
    kinds = [item["kind"] for item in items]
    assert kinds[0] == "for_you"
    assert len(kinds) == len(set(kinds))
    generated = [item for item in items if item["source"] == "generated"]
    assert [item["kind"] for item in generated if item["kind"].startswith("weekday_")] == [
        "weekday_mon",
        "weekday_tue",
        "weekday_wed",
        "weekday_thu",
        "weekday_fri",
        "weekday_sat",
        "weekday_sun",
    ]
    assert [item["weekday"] for item in generated if "weekday" in item] == list(range(7))
    for item in generated:
        ensure_mix_generator(item)


def test_worker_rejects_a_kind_outside_the_contract():
    with pytest.raises(ValueError, match="not_a_shelf"):
        generate_mix_pack(mixes=[{"kind": "not_a_shelf", "title": "Нет"}])
