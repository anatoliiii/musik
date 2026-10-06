package media

import "testing"

func TestStreamWarmEvictsOldestByTotalSize(t *testing.T) {
	cache := newStreamWarm(10)
	cache.put(1, warmEntry{bytes: make([]byte, 4)})
	cache.put(2, warmEntry{bytes: make([]byte, 4)})
	if _, ok := cache.get(1); !ok { // 1 becomes most recent
		t.Fatal("entry 1 missing")
	}
	cache.put(3, warmEntry{bytes: make([]byte, 4)})
	if _, ok := cache.get(2); ok {
		t.Fatal("least recently used entry 2 should be evicted")
	}
	if cache.bytes != 8 {
		t.Fatalf("bytes=%d, want 8", cache.bytes)
	}
	cache.put(4, warmEntry{bytes: make([]byte, 11)})
	if _, ok := cache.get(4); ok || cache.bytes != 8 {
		t.Fatalf("oversized entry must not be cached; bytes=%d", cache.bytes)
	}
}
