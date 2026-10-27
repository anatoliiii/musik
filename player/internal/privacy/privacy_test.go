package privacy

import "testing"

func TestPrivateOwnerAndVisibility(t *testing.T) {
	cases := []struct {
		path, owner string
	}{
		{"/music/Band/Album/01.flac", ""},
		{"/music/_private/p1/Yandex Music/Band/01.flac", "p1"},
		{"/mnt/other/mount/_private/p2/x.mp3", "p2"},
		{"/music/my_private/p1/x.mp3", ""},
		{"/music/_private/", ""},
		{`C:\music\_private\p3\x.mp3`, "p3"},
	}
	for _, tc := range cases {
		if got := PrivateOwner(tc.path); got != tc.owner {
			t.Errorf("PrivateOwner(%q) = %q, want %q", tc.path, got, tc.owner)
		}
	}
	if !VisibleTo("/music/a.flac", "p1") || !VisibleTo("/music/a.flac", "") {
		t.Error("shared track hidden")
	}
	if !VisibleTo("/music/_private/p1/a.flac", "p1") {
		t.Error("owner cannot see own track")
	}
	if VisibleTo("/music/_private/p1/a.flac", "p2") || VisibleTo("/music/_private/p1/a.flac", "") {
		t.Error("private track leaks")
	}
}
