package db

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	gormpostgres "gorm.io/driver/postgres"
	gorm "gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/glebarez/sqlite"
)

type Store struct {
	DB      *Database
	ORM     *gorm.DB
	Dialect string
}

const SupportedSchemaVersion = 5
const SupportedSchemaRevision = "musik_5"

func mondayZeroWeekday(day time.Weekday) int {
	return (int(day) + 6) % 7
}

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

func Open(path string) (*Store, error) {
	return OpenDatabase("", path)
}

// OpenDatabase creates the selected backend through GORM. The returned
// database compatibility port and the ORM always share the same pool.
func OpenDatabase(databaseURL, sqlitePath string) (*Store, error) {
	dialect := "sqlite"
	var orm *gorm.DB
	var err error
	if databaseURL == "" {
		// _txlock=immediate: transactions take the write lock on BEGIN. A deferred
		// transaction that reads first and then writes cannot be upgraded once another
		// writer has committed in WAL mode — SQLite fails it at once (SQLITE_BUSY /
		// BUSY_SNAPSHOT) instead of waiting for busy_timeout.
		absolute, absErr := filepath.Abs(sqlitePath)
		if absErr != nil {
			return nil, absErr
		}
		dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}).String() + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_txlock=immediate"
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
	// WAL allows concurrent readers; keep a small bounded pool on both engines.
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
			return nil, fmt.Errorf(
				"unsupported SQLite database schema version %d (player requires exactly %d); run `musik db migrate` before starting the player",
				version, SupportedSchemaVersion,
			)
		}
		var revision string
		if err := sqlDB.QueryRow(`SELECT version_num FROM alembic_version`).Scan(&revision); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("SQLite migration history is missing; run `musik db migrate` first: %w", err)
		}
		if revision != SupportedSchemaRevision {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("unsupported database migration revision %q (player requires %q)", revision, SupportedSchemaRevision)
		}
		_, _ = sqlDB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	} else {
		var revision string
		if err := sqlDB.QueryRow(`SELECT version_num FROM alembic_version`).Scan(&revision); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("PostgreSQL schema is not migrated; run `musik db migrate` first: %w", err)
		}
		if revision != SupportedSchemaRevision {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("unsupported database migration revision %q (player requires %q)", revision, SupportedSchemaRevision)
		}
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }
