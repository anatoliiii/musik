package api

import (
	"encoding/json"
	"net/http"
)

type libraryMapLabel struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Color    string  `json:"color"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	TrackIDs []int64 `json:"track_ids"`
}

type libraryMapDoc struct {
	Method    string            `json:"method"`
	UpdatedAt string            `json:"updated_at"`
	Genres    []string          `json:"genres"`
	Points    [][4]float64      `json:"points"` // [track_id, x, y, genre index or -1]
	Labels    []libraryMapLabel `json:"labels"`
}

// handleLibraryMap serves the worker-built 2D map, limited to the tracks this
// profile may see: the worker places every track, private folders included.
func (s *Server) handleLibraryMap(w http.ResponseWriter, _ *http.Request) {
	catalog, err := s.Store.ListCatalogTracks()
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	raw, err := s.Store.LibraryMapJSON()
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	doc := libraryMapDoc{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			writeErr(w, 500, "library_map", err.Error())
			return
		}
	}
	visible := make(map[int64]bool, len(catalog))
	for _, t := range catalog {
		visible[t.ID] = true
	}
	points := make([][4]float64, 0, len(doc.Points))
	for _, p := range doc.Points {
		if visible[int64(p[0])] {
			points = append(points, p)
		}
	}
	labels := make([]libraryMapLabel, 0, len(doc.Labels))
	for _, l := range doc.Labels {
		ids := make([]int64, 0, len(l.TrackIDs))
		for _, id := range l.TrackIDs {
			if visible[id] {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			continue
		}
		l.TrackIDs = ids
		labels = append(labels, l)
	}
	genres := doc.Genres
	if genres == nil {
		genres = []string{}
	}
	writeJSON(w, map[string]any{
		"ready":      raw != "",
		"method":     doc.Method,
		"updated_at": doc.UpdatedAt,
		"total":      len(catalog),
		"genres":     genres,
		"points":     points,
		"labels":     labels,
	})
}
