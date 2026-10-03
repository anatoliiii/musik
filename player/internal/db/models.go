package db

// These mapped records back portable writes shared by the player and worker.

type UserRecord struct {
	ID          string `gorm:"primaryKey"`
	Status      string `gorm:"not null"`
	DisplayName string `gorm:"column:display_name;not null"`
	CreatedAt   string `gorm:"column:created_at;not null"`
	UpdatedAt   string `gorm:"column:updated_at;not null"`
}

func (UserRecord) TableName() string { return "users" }

type UserRoleRecord struct {
	UserID string `gorm:"column:user_id;primaryKey"`
	Role   string `gorm:"primaryKey"`
}

func (UserRoleRecord) TableName() string { return "user_roles" }

type ProfileRecord struct {
	ID          string  `gorm:"primaryKey"`
	OwnerUserID string  `gorm:"column:owner_user_id;not null;index"`
	Name        string  `gorm:"not null"`
	IsDefault   int     `gorm:"column:is_default;not null"`
	CreatedAt   string  `gorm:"column:created_at;not null"`
	UpdatedAt   string  `gorm:"column:updated_at;not null"`
	DeletedAt   *string `gorm:"column:deleted_at"`
}

func (ProfileRecord) TableName() string { return "profiles" }

type AuthSessionRecord struct {
	TokenHash       string  `gorm:"column:token_hash;primaryKey"`
	UserID          string  `gorm:"column:user_id;not null"`
	ActiveProfileID string  `gorm:"column:active_profile_id;not null"`
	CSRFHash        string  `gorm:"column:csrf_hash;not null"`
	CreatedAt       string  `gorm:"column:created_at;not null"`
	ExpiresAt       string  `gorm:"column:expires_at;not null"`
	RevokedAt       *string `gorm:"column:revoked_at"`
}

func (AuthSessionRecord) TableName() string { return "auth_sessions" }

type ExternalIdentityRecord struct {
	Issuer    string `gorm:"primaryKey"`
	Subject   string `gorm:"primaryKey"`
	UserID    string `gorm:"column:user_id;not null;index"`
	CreatedAt string `gorm:"column:created_at;not null"`
}

func (ExternalIdentityRecord) TableName() string { return "external_identities" }

type InvitationRecord struct {
	ID           string  `gorm:"primaryKey"`
	SecretHash   string  `gorm:"column:secret_hash;not null;uniqueIndex"`
	CreatedBy    *string `gorm:"column:created_by"`
	Issuer       *string
	Email        *string
	CreatedAt    string  `gorm:"column:created_at;not null"`
	ExpiresAt    string  `gorm:"column:expires_at;not null"`
	ConsumedAt   *string `gorm:"column:consumed_at"`
	RevokedAt    *string `gorm:"column:revoked_at"`
	ConsumedBy   *string `gorm:"column:consumed_by"`
	TargetUserID *string `gorm:"column:target_user_id"`
}

func (InvitationRecord) TableName() string { return "invitations" }

type InstallationStateRecord struct {
	Key   string `gorm:"primaryKey"`
	Value string `gorm:"not null"`
}

func (InstallationStateRecord) TableName() string { return "installation_state" }

type JobRecord struct {
	ID          int64   `gorm:"primaryKey;autoIncrement"`
	Kind        string  `gorm:"not null"`
	Status      string  `gorm:"not null"`
	PayloadJSON *string `gorm:"column:payload_json"`
	ResultJSON  *string `gorm:"column:result_json"`
	Error       *string `gorm:"column:error"`
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
