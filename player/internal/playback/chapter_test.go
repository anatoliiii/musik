package playback

import (
	"reflect"
	"testing"
)

func TestChapterPinsCentroidUntilTheBoundary(t *testing.T) {
	centroids := [][]float32{{1, 0}, {0, 1}, {0.6, 0.8}}
	chapter, vecs := resolveChapter(TasteChapter{}, centroids, []float32{1, 0}, "morning", "track", 0.75)
	if len(vecs) != 1 || chapter.CentroidIdxs[0] != 0 {
		t.Fatalf("morning pin=%v vecs=%d", chapter.CentroidIdxs, len(vecs))
	}
	held, heldVecs := resolveChapter(chapter, centroids, []float32{0, 1}, "morning", "track", 0.75)
	if !reflect.DeepEqual(held.CentroidIdxs, chapter.CentroidIdxs) || len(heldVecs) != 1 || heldVecs[0][0] != 1 {
		t.Fatalf("taste drift retuned the chapter: %+v", held.CentroidIdxs)
	}
	retuned, _ := resolveChapter(chapter, centroids, []float32{0, 1}, "morning", "album", 0.75)
	if len(retuned.CentroidIdxs) == 0 || retuned.CentroidIdxs[0] != 1 {
		t.Fatalf("anchor change kept %v", retuned.CentroidIdxs)
	}
	daypart, _ := resolveChapter(chapter, centroids, []float32{0, 1}, "evening", "track", 0.75)
	if daypart.CentroidIdxs[0] != 1 {
		t.Fatalf("daypart kept %v", daypart.CentroidIdxs)
	}
}

func TestChapterMixesTwoCloseCentroids(t *testing.T) {
	centroids := [][]float32{{1, 0}, {0.96, 0.28}, {0, 1}}
	chapter, vecs := resolveChapter(TasteChapter{}, centroids, []float32{1, 0}, "morning", "track", 0.75)
	if len(vecs) != 2 || !reflect.DeepEqual(chapter.CentroidIdxs, []int{0, 1}) {
		t.Fatalf("mix=%v", chapter.CentroidIdxs)
	}
}

func TestClusterSkipSeriesOpensChapter(t *testing.T) {
	chapter := TasteChapter{CentroidIdxs: []int{0}, SkipCluster: 4, SkipStreak: 2}
	chapter = advanceClusterSkips(chapter, 4, 3)
	if chapter.CentroidIdxs != nil || chapter.SkipStreak != 0 {
		t.Fatalf("series did not open a chapter: %+v", chapter)
	}
	reset := advanceClusterSkips(TasteChapter{CentroidIdxs: []int{1}, SkipCluster: 4, SkipStreak: 2}, 9, 3)
	if reset.SkipStreak != 1 || reset.SkipCluster != 9 || len(reset.CentroidIdxs) != 1 {
		t.Fatalf("a different cluster restarted the streak: %+v", reset)
	}
}
