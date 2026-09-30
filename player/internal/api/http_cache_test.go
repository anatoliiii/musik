package api

import "testing"

func TestStaticCacheControl(t *testing.T) {
	cases := []struct {
		path, want string
	}{
		{"/", "no-cache"},
		{"/index.html", "no-cache"},
		{"/app.js", "public, max-age=3600"},
		{"/style.css", "public, max-age=3600"},
		{"/fonts.css", "public, max-age=3600"},
		{"/fonts/manrope-latin.woff2", "public, max-age=31536000, immutable"},
		{"/fonts/syne-latin.woff2", "public, max-age=31536000, immutable"},
		{"/favicon.ico", ""},
	}
	for _, tc := range cases {
		if got := staticCacheControl(tc.path); got != tc.want {
			t.Errorf("staticCacheControl(%q)=%q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestCompressibleType(t *testing.T) {
	if !compressibleType("application/json; charset=utf-8") {
		t.Fatal("json should compress")
	}
	if !compressibleType("text/html; charset=utf-8") {
		t.Fatal("html should compress")
	}
	if !compressibleType("text/javascript") {
		t.Fatal("js should compress")
	}
	if compressibleType("image/jpeg") {
		t.Fatal("jpeg should not compress")
	}
	if compressibleType("audio/mpeg") {
		t.Fatal("mp3 should not compress")
	}
	if compressibleType("font/woff2") {
		t.Fatal("woff2 should not compress")
	}
}
