package playback

import (
	"strconv"
	"time"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/queue"
)

// TasteChapter is the taste centroid a radio session holds until the chapter
// changes. A chapter ends on a daypart boundary, an anchor change, or a run
// of early skips inside one cluster. The queue then uses that centroid, or a
// short mix of two nearby ones, instead of every stored centroid at once.
type TasteChapter struct {
	Daypart      string `json:"daypart,omitempty"`
	AnchorKey    string `json:"anchor_key,omitempty"`
	CentroidIdxs []int  `json:"centroid_idxs,omitempty"`
	SkipCluster  int    `json:"skip_cluster,omitempty"`
	SkipStreak   int    `json:"skip_streak,omitempty"`
}

func anchorKey(a RadioAnchor) string {
	return a.Kind + "\x00" + strconv.FormatInt(a.TrackID, 10) + "\x00" + a.Artist + "\x00" + a.Album
}

func (e *Engine) centroidsForQueue(sess *Session, taste []float32) [][]float32 {
	all := e.tasteCentroidVecs()
	if sess == nil || sess.Mode != "radio" || len(all) == 0 {
		return all
	}
	chapter, vecs := resolveChapter(sess.Chapter, all, taste, db.DayPart(time.Now().Hour()), anchorKey(sess.Anchor), float32(queue.ActiveSpec.Retrieval.CentroidMix))
	sess.Chapter = chapter
	return vecs
}

func (e *Engine) noteClusterSkip(sess *Session, trackID int64) {
	if sess == nil || e == nil || e.Idx == nil || sess.Mode != "radio" {
		return
	}
	row, ok := e.Idx.RowOf(trackID)
	if !ok {
		return
	}
	cluster := e.Idx.MetaAt(row).ClusterID
	if cluster < 0 {
		return
	}
	sess.Chapter = advanceClusterSkips(sess.Chapter, cluster, queue.ActiveSpec.Retrieval.ClusterSkipSeries)
}

func resolveChapter(chapter TasteChapter, centroids [][]float32, taste []float32, daypart, anchor string, mixAt float32) (TasteChapter, [][]float32) {
	if len(centroids) == 0 {
		return chapter, nil
	}
	stale := chapter.Daypart != daypart || chapter.AnchorKey != anchor || len(chapter.CentroidIdxs) == 0
	if stale {
		chapter.Daypart = daypart
		chapter.AnchorKey = anchor
		chapter.CentroidIdxs = pickChapterCentroids(centroids, taste, mixAt)
	}
	vecs := centroidVectors(centroids, chapter.CentroidIdxs)
	if len(vecs) == 0 {
		chapter.CentroidIdxs = pickChapterCentroids(centroids, taste, mixAt)
		vecs = centroidVectors(centroids, chapter.CentroidIdxs)
	}
	return chapter, vecs
}

func advanceClusterSkips(chapter TasteChapter, cluster, limit int) TasteChapter {
	if limit < 1 {
		limit = 3
	}
	if chapter.SkipCluster == cluster && chapter.SkipStreak > 0 {
		chapter.SkipStreak++
	} else {
		chapter.SkipCluster = cluster
		chapter.SkipStreak = 1
	}
	if chapter.SkipStreak >= limit {
		chapter.CentroidIdxs = nil
		chapter.SkipStreak = 0
	}
	return chapter
}

func pickChapterCentroids(centroids [][]float32, taste []float32, mixAt float32) []int {
	if len(centroids) == 0 {
		return nil
	}
	if len(centroids) == 1 || len(taste) == 0 {
		return []int{0}
	}
	bestI, secondI := 0, -1
	best, second := float32(-2), float32(-2)
	for i, centroid := range centroids {
		if len(centroid) != len(taste) {
			continue
		}
		sim := dot32(centroid, taste)
		if sim > best {
			second, secondI = best, bestI
			best, bestI = sim, i
			continue
		}
		if sim > second {
			second, secondI = sim, i
		}
	}
	out := []int{bestI}
	if secondI >= 0 && mixAt > 0 && len(centroids[bestI]) == len(centroids[secondI]) && dot32(centroids[bestI], centroids[secondI]) >= mixAt {
		out = append(out, secondI)
	}
	return out
}

func centroidVectors(centroids [][]float32, idxs []int) [][]float32 {
	out := make([][]float32, 0, len(idxs))
	seen := map[int]bool{}
	for _, idx := range idxs {
		if idx < 0 || idx >= len(centroids) || seen[idx] {
			continue
		}
		seen[idx] = true
		out = append(out, centroids[idx])
	}
	return out
}

func dot32(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}
