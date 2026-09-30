package apitest

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/torwin-job/musik/player/internal/db"
)

func TestLibraryFiltersByArtistAndAlbum(t *testing.T) {
	server := openTestServer(t)
	if _, err := server.Store.DB.Exec(`DELETE FROM tracks`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []db.TrackRow{
		{ID: 1, Path: "/tmp/one.flac", Title: "One", Artist: "Massive Attack", Album: "Mezzanine", Duration: 180},
		{ID: 2, Path: "/tmp/two.flac", Title: "Two", Artist: "Massive Attack", Album: "Protection", Duration: 180},
		{ID: 3, Path: "/tmp/three.flac", Title: "Three", Artist: "Portishead", Album: "Dummy", Duration: 180},
	} {
		if _, err := server.Store.DB.Exec(
			`INSERT INTO tracks(id, path, title, artist, album, duration) VALUES (?,?,?,?,?,?)`,
			row.ID, row.Path, row.Title, row.Artist, row.Album, row.Duration,
		); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name  string
		query url.Values
		want  []int64
	}{
		{name: "all", want: []int64{1, 2, 3}},
		{name: "artist case insensitive", query: url.Values{"artist": {" massive attack "}}, want: []int64{1, 2}},
		{name: "artist and album", query: url.Values{"artist": {"Massive Attack"}, "album": {"mezzanine"}}, want: []int64{1}},
		{name: "unknown artist", query: url.Values{"artist": {"Unknown"}}, want: []int64{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := serve(server, jsonReq("GET", "/api/library?"+test.query.Encode(), ""))
			if rec.Code != 200 {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			var rows []struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			got := make([]int64, 0, len(rows))
			for _, row := range rows {
				got = append(got, row.ID)
			}
			if len(got) != len(test.want) {
				t.Fatalf("ids = %v, want %v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("ids = %v, want %v", got, test.want)
				}
			}
		})
	}
}

func TestTrackPassport(t *testing.T) {
	server := openTestServer(t)
	if _, err := server.Store.DB.Exec(`DELETE FROM tracks`); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Store.DB.Exec(
		`INSERT INTO tracks(id, path, title, artist, album, duration, lufs, bitrate, sample_rate, channels)
		 VALUES (1, '/tmp/one.flac', 'One', 'Artist A', 'Album A', 180, -15, 900000, 44100, 2)`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Store.DB.Exec(
		`INSERT INTO features(track_id, status, bpm, key_name, mode, lufs) VALUES (1, 'ready', 128, 'A', 'minor', -14.5)`,
	); err != nil {
		t.Fatal(err)
	}
	rec := serve(server, jsonReq("GET", "/api/tracks/1", ""))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Passport *struct {
			Format     string  `json:"format"`
			Bitrate    int64   `json:"bitrate"`
			SampleRate int64   `json:"sample_rate"`
			Channels   int64   `json:"channels"`
			LUFS       float64 `json:"lufs"`
			BPM        float64 `json:"bpm"`
			Key        string  `json:"key"`
			Mode       string  `json:"mode"`
		} `json:"passport"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	p := got.Passport
	if p == nil {
		t.Fatalf("no passport in %s", rec.Body.String())
	}
	if p.Format != "FLAC" || p.Bitrate != 900000 || p.SampleRate != 44100 || p.Channels != 2 ||
		p.LUFS != -14.5 || p.BPM != 128 || p.Key != "A" || p.Mode != "minor" {
		t.Fatalf("passport = %+v", *p)
	}
}
