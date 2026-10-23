package playback

import "errors"

// Sentinel errors for play and session commands. HTTP maps these with
// errors.Is so a wrapped or translated message still gets the right status.
var (
	ErrNoTracks           = errors.New("нет доступных треков")
	ErrNothingFound       = errors.New("ничего не найдено")
	ErrTrackNotFound      = errors.New("track not found")
	ErrTrackNotInPlaylist = errors.New("track not in playlist")
	ErrTrackNotInQueue    = errors.New("track not in queue")
	ErrNoPrevious         = errors.New("нет предыдущего трека")
	ErrPlaySpec           = errors.New("укажи track_id, track_ids, artist или album")
)
