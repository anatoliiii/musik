package db

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	gormpostgres "gorm.io/driver/postgres"
	gorm "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Store struct {
	DB      *Database
	ORM     *gorm.DB
	Dialect string
}

const SupportedSchemaVersion = 7
const SupportedSchemaRevision = "musik_7"

func mondayZeroWeekday(day time.Weekday) int { return (int(day) + 6) % 7 }

type TrackRow struct {
	ID           int64
	Path         string
	Title        string
	Artist       string
	Album        string
	Duration     float64
	FileMD5      string
	CreatedAt    string
	ArtworkPath  string
	ClusterID    int
	Embedding    []byte
	Dim          int
	Shown        int
	SkipEarly    int
	Completed    int
	Year         int
	BPM          float64
	LUFS         float64
	HasBPM       bool
	HasLUFS      bool
	KeyName      string
	Plays        int
	Finishes     int
	EarlySkips   int
	LastPlayedAt string
}

func Open(path string) (*Store, error) { return OpenDatabase("", path) }

// OpenDatabase opens either configured backend through GORM. The compatibility
// repository port shares GORM's pool and carries the active profile binding.
func OpenDatabase(databaseURL, sqlitePath string) (*Store, error) {
	dialect := "sqlite"
	var orm *gorm.DB
	var err error
	if databaseURL == "" {
		absolute, absErr := filepath.Abs(sqlitePath)
		if absErr != nil {
			return nil, absErr
		}
		dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}).String() +
			"?_pragma=foreign_keys(1)&_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_txlock=immediate"
		orm, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{
			Logger:                                   logger.Default.LogMode(logger.Silent),
			DisableForeignKeyConstraintWhenMigrating: true,
		})
	} else {
		u, parseErr := url.Parse(databaseURL)
		if parseErr != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" || strings.Trim(u.Path, "/") == "" {
			return nil, fmt.Errorf("invalid PostgreSQL MUSIK_DATABASE_URL")
		}
		dialect = "postgres"
		orm, err = gorm.Open(gormpostgres.Open(databaseURL), &gorm.Config{
			Logger:                                   logger.Default.LogMode(logger.Silent),
			DisableForeignKeyConstraintWhenMigrating: true,
		})
	}
	if err != nil {
		return nil, err
	}
	sqlDB, err := orm.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(8)
	sqlDB.SetMaxIdleConns(4)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	s := &Store{DB: &Database{DB: sqlDB, Dialect: dialect}, ORM: orm, Dialect: dialect}
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if dialect == "sqlite" {
		var version int
		if err := sqlDB.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("read SQLite schema version: %w", err)
		}
		if version != SupportedSchemaVersion {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("unsupported SQLite database schema version %d (player requires exactly %d); run `musik db migrate` before starting the player", version, SupportedSchemaVersion)
		}
		_, _ = sqlDB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	var revision string
	if err := sqlDB.QueryRow(`SELECT version_num FROM alembic_version`).Scan(&revision); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("database migration history is missing; run `musik db migrate` first: %w", err)
	}
	if revision != SupportedSchemaRevision {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("unsupported database migration revision %q (player requires %q)", revision, SupportedSchemaRevision)
	}
	if err := sqlDB.QueryRow(`SELECT value FROM installation_state WHERE key='legacy_profile_id'`).Scan(&s.DB.ProfileID); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("read installation profile: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) ForProfile(profileID string) *Store {
	database := *s.DB
	database.ProfileID = profileID
	return &Store{DB: &database, ORM: s.ORM.Session(&gorm.Session{}), Dialect: s.Dialect}
}
