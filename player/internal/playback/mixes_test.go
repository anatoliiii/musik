package playback

import "testing"

func TestMixRegistryIsTheShelfAndTheWorkerContract(t *testing.T) {
	if len(Mixes) == 0 {
		t.Fatal("mix registry is empty")
	}
	seen := map[string]bool{}
	var weekdays []int
	for _, mix := range Mixes {
		if mix.Kind == "" || mix.Title == "" || seen[mix.Kind] {
			t.Fatalf("bad shelf entry %+v", mix)
		}
		seen[mix.Kind] = true
		switch mix.Source {
		case MixSourceGenerated, MixSourceLater, MixSourceFavorites:
		default:
			t.Fatalf("kind %s source %q", mix.Kind, mix.Source)
		}
		if GeneratedMixKind(mix.Kind) != (mix.Source == MixSourceGenerated) {
			t.Fatalf("GeneratedMixKind(%s) disagrees with source %s", mix.Kind, mix.Source)
		}
		if mix.Weekday != nil {
			weekdays = append(weekdays, *mix.Weekday)
		}
	}
	if GeneratedMixKind("favorites") || GeneratedMixKind("later") || GeneratedMixKind("user") {
		t.Fatal("saved lists must not be generated mixes")
	}
	if !GeneratedMixKind("for_you") || !GeneratedMixKind("weekday_mon") {
		t.Fatal("worker shelves dropped out of GeneratedMixKind")
	}
	if len(weekdays) != 7 {
		t.Fatalf("weekdays=%v", weekdays)
	}
	for i, day := range weekdays {
		if day != i {
			t.Fatalf("weekday %d stored as %d", i, day)
		}
	}
	contract := GeneratedMixContract()
	if contract[0].Kind != "for_you" || contract[0].Weekday != nil {
		t.Fatalf("contract start %+v", contract[0])
	}
	last := contract[len(contract)-1]
	if last.Kind != "weekday_sun" || last.Weekday == nil || *last.Weekday != 6 {
		t.Fatalf("contract end %+v", last)
	}
	for _, item := range contract {
		if !GeneratedMixKind(item.Kind) {
			t.Fatalf("worker contract includes %s", item.Kind)
		}
	}
}
