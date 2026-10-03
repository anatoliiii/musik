package db

// These mapped records back portable writes shared by the player and worker.

type JobRecord struct {
	ID          int64   `gorm:"primaryKey;autoIncrement"`
	Kind        string  `gorm:"not null"`
	Status      string  `gorm:"not null"`
	PayloadJSON *string `gorm:"column:payload_json"`
	CreatedAt   string  `gorm:"not null"`
	UpdatedAt   string  `gorm:"not null"`
}

func (JobRecord) TableName() string { return "jobs" }

type ListenRecord struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	ProfileID   string `gorm:"column:profile_id;not null;index"`
	TrackID     int64  `gorm:"not null"`
	TS          string `gorm:"column:ts;not null"`
	Source      *string
	Action      string `gorm:"not null"`
	Daypart     *string
	Weekday     *int
	PositionSec *float64
	DurationSec *float64
	ListenedSec *float64
	SessionID   *string `gorm:"column:session_id"`
	Reason      *string
}

func (ListenRecord) TableName() string { return "listening_history" }

type PlaylistRecord struct {
	ID                int64  `gorm:"primaryKey;autoIncrement"`
	ProfileID         string `gorm:"column:profile_id;not null;index"`
	Kind              string `gorm:"not null"`
	Name              string `gorm:"not null"`
	CreatedAt         string `gorm:"not null"`
	Type              string
	Description       *string
	UpdatedAt         *string
	CoverTrackID      *int64 `gorm:"column:cover_track_id"`
	CoverArtwork      *string
	SortMode          string  `gorm:"column:sort_mode"`
	RuleSchemaVersion *int64  `gorm:"column:rule_schema_version"`
	RuleJSON          *string `gorm:"column:rule_json"`
	AllowDuplicates   int     `gorm:"column:allow_duplicates"`
}

func (PlaylistRecord) TableName() string { return "playlists" }

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
