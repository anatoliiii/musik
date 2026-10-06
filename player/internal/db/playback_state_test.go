package db

import (
	"context"
	"testing"
)

func TestPlaybackStateBelongsToOneProfile(t *testing.T) {
	store, _, _ := openTestTarget(t)
	insertTracks(t, store, 1)
	_, other, err := store.CreateUserWithDefaultProfile(context.Background(), "Other")
	if err != nil {
		t.Fatal(err)
	}
	peer := store.ForProfile(other.ID)
	if _, applied, err := store.SavePlaybackState(PlaybackState{TrackID: 1, PositionSec: 42, Playing: true, ClientID: "phone"}, true); err != nil || !applied {
		t.Fatalf("save applied=%v err=%v", applied, err)
	}
	if _, ok, err := peer.LoadPlaybackState(); err != nil || ok {
		t.Fatalf("another profile sees the state: ok=%v err=%v", ok, err)
	}
	// A heartbeat from a device that does not own the row is not applied.
	if _, applied, err := store.SavePlaybackState(PlaybackState{TrackID: 1, PositionSec: 50, ClientID: "laptop"}, false); err != nil || applied {
		t.Fatalf("foreign heartbeat applied=%v err=%v", applied, err)
	}
	st, ok, err := store.LoadPlaybackState()
	if err != nil || !ok || st.PositionSec != 42 || st.ClientID != "phone" {
		t.Fatalf("state=%+v ok=%v err=%v", st, ok, err)
	}
}
