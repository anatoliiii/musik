// Package testdb creates the explicit SQL fixture used by Go tests. Production
// code never creates or migrates the shared database.
package testdb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/glebarez/go-sqlite"
)

const Schema = `
PRAGMA foreign_keys = OFF;
BEGIN TRANSACTION;
CREATE TABLE auth_sessions (
 token_hash TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id),
 active_profile_id TEXT NOT NULL, csrf_hash TEXT NOT NULL,
 created_at TEXT NOT NULL, expires_at TEXT NOT NULL, revoked_at TEXT,
 FOREIGN KEY(active_profile_id,user_id) REFERENCES profiles(id,owner_user_id)
);
CREATE TABLE "custom_tags" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
tag_id TEXT PRIMARY KEY,
name TEXT NOT NULL,
created_at TEXT NOT NULL,
archived_at TEXT,
owner_scope TEXT NOT NULL DEFAULT 'local',
UNIQUE(profile_id,tag_id));
CREATE TABLE "discover_tips" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
id INTEGER PRIMARY KEY AUTOINCREMENT,
kind TEXT NOT NULL,
artist TEXT,
album TEXT,
score REAL NOT NULL DEFAULT 0,
track_ids_json TEXT NOT NULL,
explanation TEXT,
created_at TEXT NOT NULL,
UNIQUE(profile_id,id));
CREATE TABLE entity_vectors (
 entity_type TEXT NOT NULL, entity_key TEXT NOT NULL, embedding BLOB NOT NULL,
 embedding_dim INTEGER NOT NULL, model_version TEXT NOT NULL,
 track_count INTEGER NOT NULL, computed_at TEXT NOT NULL,
 PRIMARY KEY (entity_type, entity_key, model_version)
);
CREATE TABLE "event_contexts" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
history_id INTEGER NOT NULL,
context_id TEXT NOT NULL,
PRIMARY KEY(profile_id,history_id,context_id),
CHECK(history_id IS NOT NULL AND context_id IS NOT NULL),
FOREIGN KEY(profile_id,history_id) REFERENCES listening_history(profile_id,id),
FOREIGN KEY(profile_id,context_id) REFERENCES taste_contexts(profile_id,context_id));
CREATE TABLE "explore_arms" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
arm_key TEXT ,
arm_kind TEXT NOT NULL,
alpha REAL NOT NULL,
beta REAL NOT NULL,
successes INTEGER NOT NULL DEFAULT 0,
failures INTEGER NOT NULL DEFAULT 0,
last_decay_at TEXT,
updated_at TEXT NOT NULL,
owner_scope TEXT NOT NULL DEFAULT 'local',
PRIMARY KEY(profile_id,arm_key),
CHECK(arm_key IS NOT NULL));
CREATE TABLE external_identities (
 issuer TEXT NOT NULL, subject TEXT NOT NULL, user_id TEXT NOT NULL REFERENCES users(id),
 created_at TEXT NOT NULL, PRIMARY KEY(issuer,subject)
);
CREATE TABLE "favorite_albums" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
artist TEXT NOT NULL,
album TEXT NOT NULL,
added_at TEXT NOT NULL,
position INTEGER NOT NULL DEFAULT 0,
PRIMARY KEY(profile_id,artist,album),
CHECK(artist IS NOT NULL AND album IS NOT NULL));
CREATE TABLE "favorite_artists" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
artist TEXT ,
added_at TEXT NOT NULL,
position INTEGER NOT NULL DEFAULT 0,
PRIMARY KEY(profile_id,artist),
CHECK(artist IS NOT NULL));
CREATE TABLE "favorites" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
track_id INTEGER ,
added_at TEXT NOT NULL,
position INTEGER NOT NULL DEFAULT 0,
PRIMARY KEY(profile_id,track_id),
CHECK(track_id IS NOT NULL));
CREATE TABLE "feature_weights" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
week_key TEXT NOT NULL,
dim INTEGER NOT NULL,
weight REAL NOT NULL,
PRIMARY KEY(profile_id,week_key,dim),
CHECK(week_key IS NOT NULL AND dim IS NOT NULL));
CREATE TABLE features (
 track_id INTEGER PRIMARY KEY, status TEXT, cluster_id INTEGER, embedding BLOB,
 embedding_dim INTEGER, bpm REAL, key_name TEXT, mode TEXT, lufs REAL
);
CREATE TABLE installation_state (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO "installation_state" VALUES('legacy_user_id','330a64b2-4303-4813-bdc1-495b869c15a7');
INSERT INTO "installation_state" VALUES('legacy_profile_id','8458c014-517b-401b-93de-54c6ab327596');
INSERT INTO "installation_state" VALUES('active_admin_guard','1');
CREATE TABLE invitations (
 id TEXT PRIMARY KEY, secret_hash TEXT NOT NULL UNIQUE,
 created_by TEXT REFERENCES users(id), issuer TEXT, email TEXT,
 created_at TEXT NOT NULL, expires_at TEXT NOT NULL, consumed_at TEXT,
 revoked_at TEXT, consumed_by TEXT REFERENCES users(id)
, target_user_id TEXT REFERENCES users(id));
CREATE TABLE device_tokens (
 id TEXT PRIMARY KEY,
 secret_hash TEXT NOT NULL UNIQUE,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 profile_id TEXT NOT NULL REFERENCES profiles(id),
 name TEXT NOT NULL,
 created_at TEXT NOT NULL,
 last_used_at TEXT,
 expires_at TEXT NOT NULL,
 revoked_at TEXT
);
CREATE INDEX idx_device_tokens_user ON device_tokens(user_id, revoked_at, expires_at);
CREATE TABLE jobs (
 id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
 payload_json TEXT, result_json TEXT, error TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE "listen_later" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
track_id INTEGER ,
added_at TEXT NOT NULL,
position INTEGER NOT NULL DEFAULT 0,
PRIMARY KEY(profile_id,track_id),
CHECK(track_id IS NOT NULL));
CREATE TABLE "listening_history" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
id INTEGER PRIMARY KEY AUTOINCREMENT,
track_id INTEGER NOT NULL,
ts TEXT NOT NULL,
source TEXT,
action TEXT NOT NULL,
daypart TEXT,
weekday INTEGER,
position_sec REAL,
duration_sec REAL,
listened_sec REAL,
session_id TEXT,
reason TEXT,
event_id TEXT,
event_schema_version INTEGER NOT NULL DEFAULT 1,
request_id TEXT,
impression_id TEXT,
device_id TEXT,
client_id TEXT,
metadata_schema_version INTEGER,
metadata_json TEXT,
UNIQUE(profile_id,id),
FOREIGN KEY(profile_id,request_id) REFERENCES recommendation_requests(profile_id,request_id));
CREATE TABLE lyrics (
 track_id INTEGER PRIMARY KEY, plain_lyrics TEXT NOT NULL DEFAULT '',
 synced_lyrics TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '',
 source_id TEXT NOT NULL DEFAULT '', instrumental INTEGER NOT NULL DEFAULT 0,
 status TEXT NOT NULL DEFAULT 'pending', error TEXT, updated_at TEXT NOT NULL
);
CREATE TABLE "model_versions" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
model_version TEXT ,
model_type TEXT NOT NULL,
feature_schema_version INTEGER NOT NULL,
artifact_path TEXT NOT NULL,
artifact_hash TEXT NOT NULL,
status TEXT NOT NULL,
created_at TEXT NOT NULL,
activated_at TEXT,
PRIMARY KEY(profile_id,model_version),
CHECK(model_version IS NOT NULL));
CREATE TABLE "play_sessions" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
id TEXT PRIMARY KEY,
mode TEXT NOT NULL DEFAULT '',
current_id INTEGER NOT NULL DEFAULT 0,
queue_json TEXT,
exclude_json TEXT,
rated_json TEXT,
daily_ids_json TEXT,
daily_pos INTEGER NOT NULL DEFAULT 0,
playlist_name TEXT NOT NULL DEFAULT '',
playlist_kind TEXT NOT NULL DEFAULT '',
updated_at TEXT NOT NULL,
current_item_json TEXT,
taste_state_schema_version INTEGER,
taste_state_json TEXT,
active_contexts_json TEXT,
transition_profile TEXT NOT NULL DEFAULT 'smooth',
UNIQUE(profile_id,id));
CREATE TABLE "playlist_tracks" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
item_id TEXT,
playlist_id INTEGER NOT NULL,
position INTEGER NOT NULL,
track_id INTEGER,
unresolved_artist TEXT,
unresolved_title TEXT,
unresolved_path TEXT,
added_at TEXT NOT NULL DEFAULT '',
source TEXT NOT NULL DEFAULT 'manual',
note TEXT,
explanation TEXT,
PRIMARY KEY(profile_id,playlist_id,position),
CHECK(playlist_id IS NOT NULL AND position IS NOT NULL),
FOREIGN KEY(profile_id,playlist_id) REFERENCES playlists(profile_id,id));
CREATE TABLE "playlists" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
id INTEGER PRIMARY KEY,
kind TEXT NOT NULL,
name TEXT NOT NULL,
created_at TEXT NOT NULL,
meta_json TEXT,
type TEXT NOT NULL DEFAULT 'generated',
description TEXT,
updated_at TEXT,
cover_track_id INTEGER,
cover_artwork TEXT,
sort_mode TEXT NOT NULL DEFAULT 'manual',
archived_at TEXT,
rule_schema_version INTEGER,
rule_json TEXT,
allow_duplicates INTEGER NOT NULL DEFAULT 0,
owner_scope TEXT NOT NULL DEFAULT 'local',
UNIQUE(profile_id,id));
CREATE TABLE profiles (
 id TEXT PRIMARY KEY, owner_user_id TEXT NOT NULL REFERENCES users(id),
 name TEXT NOT NULL, is_default INTEGER NOT NULL DEFAULT 0 CHECK(is_default IN (0,1)),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, deleted_at TEXT,
 UNIQUE(id,owner_user_id)
);
INSERT INTO "profiles" VALUES('8458c014-517b-401b-93de-54c6ab327596','330a64b2-4303-4813-bdc1-495b869c15a7','Main',1,'2026-10-03T12:35:55.034610+00:00','2026-10-03T12:35:55.034610+00:00',NULL);
CREATE TABLE "radio_prefs" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
owner_scope TEXT  DEFAULT 'local',
explore_lo REAL NOT NULL DEFAULT 0.10,
explore_hi REAL NOT NULL DEFAULT 0.40,
updated_at TEXT NOT NULL,
PRIMARY KEY(profile_id,owner_scope),
CHECK(owner_scope IS NOT NULL));
CREATE TABLE "radio_rules" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
rule_id TEXT PRIMARY KEY,
target_type TEXT NOT NULL,
action TEXT NOT NULL,
scope TEXT NOT NULL,
target_key TEXT NOT NULL,
strength REAL NOT NULL,
session_id TEXT,
context_id TEXT,
expires_at TEXT,
created_at TEXT NOT NULL,
archived_at TEXT,
UNIQUE(profile_id,rule_id),
FOREIGN KEY(profile_id,context_id) REFERENCES taste_contexts(profile_id,context_id));
CREATE TABLE "radio_shares" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
token TEXT PRIMARY KEY,
name TEXT NOT NULL DEFAULT '',
created_at TEXT NOT NULL,
revoked_at TEXT,
last_listen_at TEXT,
listen_count INTEGER NOT NULL DEFAULT 0,
UNIQUE(profile_id,token));
CREATE TABLE "rec_stats" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
track_id INTEGER ,
shown INTEGER NOT NULL DEFAULT 0,
skipped_early INTEGER NOT NULL DEFAULT 0,
completed INTEGER NOT NULL DEFAULT 0,
updated_at TEXT NOT NULL,
PRIMARY KEY(profile_id,track_id),
CHECK(track_id IS NOT NULL));
CREATE TABLE "recommendation_impressions" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
id INTEGER PRIMARY KEY AUTOINCREMENT,
session_id TEXT NOT NULL,
track_id INTEGER NOT NULL,
position INTEGER NOT NULL,
score REAL NOT NULL DEFAULT 0,
cosine_taste REAL NOT NULL DEFAULT 0,
cosine_current REAL NOT NULL DEFAULT 0,
explore INTEGER NOT NULL DEFAULT 0,
new_boost INTEGER NOT NULL DEFAULT 0,
maturity TEXT NOT NULL DEFAULT '',
mode TEXT NOT NULL DEFAULT '',
shown_at TEXT NOT NULL,
impression_id TEXT NOT NULL UNIQUE,
request_id TEXT,
source TEXT NOT NULL,
features_schema_version INTEGER,
features_json TEXT,
queued_at TEXT NOT NULL,
played_at TEXT,
closed_at TEXT,
outcome TEXT NOT NULL DEFAULT 'pending',
listened_ratio REAL,
legacy INTEGER NOT NULL DEFAULT 0,
UNIQUE(profile_id,id),
FOREIGN KEY(profile_id,request_id) REFERENCES recommendation_requests(profile_id,request_id));
CREATE TABLE "recommendation_requests" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
request_id TEXT PRIMARY KEY,
session_id TEXT NOT NULL,
reason TEXT NOT NULL,
policy_version TEXT NOT NULL,
model_version TEXT,
candidate_count INTEGER NOT NULL,
latency_ms REAL NOT NULL,
created_at TEXT NOT NULL,
policy_schema_version INTEGER,
policy_json TEXT,
UNIQUE(profile_id,request_id));
CREATE TABLE "request_contexts" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
request_id TEXT NOT NULL,
context_id TEXT NOT NULL,
PRIMARY KEY(profile_id,request_id,context_id),
CHECK(request_id IS NOT NULL AND context_id IS NOT NULL),
FOREIGN KEY(profile_id,request_id) REFERENCES recommendation_requests(profile_id,request_id),
FOREIGN KEY(profile_id,context_id) REFERENCES taste_contexts(profile_id,context_id));
CREATE TABLE schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE "session_contexts" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
session_id TEXT NOT NULL,
context_id TEXT NOT NULL,
activated_at TEXT NOT NULL,
deactivated_at TEXT,
PRIMARY KEY(profile_id,session_id,context_id,activated_at),
CHECK(session_id IS NOT NULL AND context_id IS NOT NULL AND activated_at IS NOT NULL),
FOREIGN KEY(profile_id,context_id) REFERENCES taste_contexts(profile_id,context_id));
CREATE TABLE "taste_centroids" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
idx INTEGER NOT NULL,
vector BLOB NOT NULL,
embedding_dim INTEGER NOT NULL,
mass REAL NOT NULL,
label TEXT,
sample_count INTEGER NOT NULL,
updated_at TEXT NOT NULL,
algorithm_version TEXT NOT NULL,
PRIMARY KEY(profile_id,idx,algorithm_version),
CHECK(idx IS NOT NULL AND algorithm_version IS NOT NULL));
CREATE TABLE "taste_context_states" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
context_id TEXT ,
positive_vector BLOB,
embedding_dim INTEGER,
negative_schema_version INTEGER,
negative_prototypes_json TEXT,
positive_samples INTEGER NOT NULL DEFAULT 0,
negative_samples INTEGER NOT NULL DEFAULT 0,
model_version TEXT NOT NULL,
updated_at TEXT NOT NULL,
PRIMARY KEY(profile_id,context_id),
CHECK(context_id IS NOT NULL),
FOREIGN KEY(profile_id,context_id) REFERENCES taste_contexts(profile_id,context_id));
CREATE TABLE "taste_contexts" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
context_id TEXT PRIMARY KEY,
kind TEXT NOT NULL,
name TEXT NOT NULL,
icon TEXT,
influence REAL NOT NULL DEFAULT 1,
learning_enabled INTEGER NOT NULL DEFAULT 1,
activation_schema_version INTEGER,
activation_json TEXT,
seeds_schema_version INTEGER,
seeds_json TEXT,
created_at TEXT NOT NULL,
updated_at TEXT NOT NULL,
archived_at TEXT,
UNIQUE(profile_id,context_id));
CREATE TABLE "taste_states" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
state_key TEXT ,
positive_vector BLOB,
embedding_dim INTEGER,
positive_samples INTEGER NOT NULL DEFAULT 0,
negative_samples INTEGER NOT NULL DEFAULT 0,
negative_schema_version INTEGER,
negative_prototypes_json TEXT,
model_version TEXT NOT NULL DEFAULT 'clap-default',
updated_at TEXT NOT NULL,
PRIMARY KEY(profile_id,state_key),
CHECK(state_key IS NOT NULL));
CREATE TABLE "track_preferences" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
track_id INTEGER ,
rating TEXT NOT NULL DEFAULT 'neutral',
favorite INTEGER NOT NULL DEFAULT 0,
updated_at TEXT NOT NULL,
PRIMARY KEY(profile_id,track_id),
CHECK(track_id IS NOT NULL));
CREATE TABLE "track_stats" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
track_id INTEGER ,
plays INTEGER NOT NULL DEFAULT 0,
finishes INTEGER NOT NULL DEFAULT 0,
partial INTEGER NOT NULL DEFAULT 0,
early_skips INTEGER NOT NULL DEFAULT 0,
likes INTEGER NOT NULL DEFAULT 0,
dislikes INTEGER NOT NULL DEFAULT 0,
last_played_at TEXT,
last_finished_at TEXT,
updated_at TEXT NOT NULL,
PRIMARY KEY(profile_id,track_id),
CHECK(track_id IS NOT NULL));
CREATE TABLE "track_tags" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
tag_id TEXT NOT NULL,
track_id INTEGER NOT NULL,
created_at TEXT NOT NULL,
PRIMARY KEY(profile_id,tag_id,track_id),
CHECK(tag_id IS NOT NULL AND track_id IS NOT NULL),
FOREIGN KEY(profile_id,tag_id) REFERENCES custom_tags(profile_id,tag_id));
CREATE TABLE tracks (
 id INTEGER PRIMARY KEY, path TEXT NOT NULL DEFAULT '', file_md5 TEXT, title TEXT,
 artist TEXT, album TEXT, year INTEGER, duration REAL, lufs REAL,
 bitrate INTEGER, sample_rate INTEGER, channels INTEGER,
 is_active INTEGER NOT NULL DEFAULT 1,
 is_duplicate_of INTEGER, artwork_path TEXT, created_at TEXT
);
CREATE TABLE "training_runs" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
run_id TEXT PRIMARY KEY,
model_version TEXT,
model_type TEXT NOT NULL,
feature_schema_version INTEGER NOT NULL,
train_from TEXT NOT NULL,
train_until TEXT NOT NULL,
positive_count INTEGER NOT NULL,
negative_count INTEGER NOT NULL,
metrics_schema_version INTEGER NOT NULL,
metrics_json TEXT NOT NULL,
status TEXT NOT NULL,
created_at TEXT NOT NULL,
completed_at TEXT,
UNIQUE(profile_id,run_id),
FOREIGN KEY(profile_id,model_version) REFERENCES model_versions(profile_id,model_version));
CREATE TABLE "transition_stats" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
from_id INTEGER NOT NULL,
to_id INTEGER NOT NULL,
manual_count INTEGER NOT NULL DEFAULT 0,
radio_count INTEGER NOT NULL DEFAULT 0,
finished_count INTEGER NOT NULL DEFAULT 0,
partial_count INTEGER NOT NULL DEFAULT 0,
skip_count INTEGER NOT NULL DEFAULT 0,
decayed_weight REAL NOT NULL DEFAULT 0,
updated_at TEXT NOT NULL,
PRIMARY KEY(profile_id,from_id,to_id),
CHECK(from_id IS NOT NULL AND to_id IS NOT NULL));
CREATE TABLE "transitions" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
from_id INTEGER NOT NULL,
to_id INTEGER NOT NULL,
weight REAL NOT NULL DEFAULT 1,
updated_at TEXT NOT NULL,
PRIMARY KEY(profile_id,from_id,to_id),
CHECK(from_id IS NOT NULL AND to_id IS NOT NULL));
CREATE TABLE "user_profile_snapshots" (profile_id TEXT NOT NULL DEFAULT '8458c014-517b-401b-93de-54c6ab327596' REFERENCES profiles(id),
id INTEGER PRIMARY KEY AUTOINCREMENT,
context TEXT NOT NULL,
embedding BLOB NOT NULL,
created_at TEXT NOT NULL,
UNIQUE(profile_id,id));
CREATE TABLE user_roles (
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 role TEXT NOT NULL CHECK(role IN ('user','admin')), PRIMARY KEY(user_id,role)
);
INSERT INTO "user_roles" VALUES('330a64b2-4303-4813-bdc1-495b869c15a7','admin');
INSERT INTO "user_roles" VALUES('330a64b2-4303-4813-bdc1-495b869c15a7','user');
CREATE TABLE users (
 id TEXT PRIMARY KEY, status TEXT NOT NULL CHECK(status IN ('active','disabled')),
 display_name TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
INSERT INTO "users" VALUES('330a64b2-4303-4813-bdc1-495b869c15a7','disabled','Installation owner','2026-10-03T12:35:55.034610+00:00','2026-10-03T12:35:55.034610+00:00');
CREATE UNIQUE INDEX idx_profiles_one_default ON profiles(owner_user_id)
 WHERE is_default=1 AND deleted_at IS NULL;
CREATE INDEX "idx_custom_tags_profile" ON "custom_tags"(profile_id);
CREATE INDEX "idx_discover_tips_profile" ON "discover_tips"(profile_id);
CREATE INDEX "idx_event_contexts_profile" ON "event_contexts"(profile_id);
CREATE INDEX "idx_explore_arms_profile" ON "explore_arms"(profile_id);
CREATE INDEX "idx_favorite_albums_profile" ON "favorite_albums"(profile_id);
CREATE INDEX "idx_favorite_artists_profile" ON "favorite_artists"(profile_id);
CREATE INDEX "idx_favorites_profile" ON "favorites"(profile_id);
CREATE INDEX "idx_feature_weights_profile" ON "feature_weights"(profile_id);
CREATE INDEX "idx_listen_later_profile" ON "listen_later"(profile_id);
CREATE UNIQUE INDEX idx_listening_history_event_id ON listening_history(event_id) WHERE event_id IS NOT NULL;
CREATE INDEX "idx_listening_history_profile" ON "listening_history"(profile_id);
CREATE INDEX "idx_model_versions_profile" ON "model_versions"(profile_id);
CREATE INDEX "idx_play_sessions_profile" ON "play_sessions"(profile_id);
CREATE INDEX "idx_playlist_tracks_profile" ON "playlist_tracks"(profile_id);
CREATE INDEX "idx_playlists_profile" ON "playlists"(profile_id);
CREATE INDEX "idx_radio_prefs_profile" ON "radio_prefs"(profile_id);
CREATE INDEX "idx_radio_rules_profile" ON "radio_rules"(profile_id);
CREATE INDEX "idx_radio_shares_profile" ON "radio_shares"(profile_id);
CREATE INDEX "idx_rec_stats_profile" ON "rec_stats"(profile_id);
CREATE INDEX "idx_recommendation_impressions_profile" ON "recommendation_impressions"(profile_id);
CREATE INDEX "idx_recommendation_requests_profile" ON "recommendation_requests"(profile_id);
CREATE INDEX "idx_request_contexts_profile" ON "request_contexts"(profile_id);
CREATE INDEX "idx_session_contexts_profile" ON "session_contexts"(profile_id);
CREATE INDEX "idx_taste_centroids_profile" ON "taste_centroids"(profile_id);
CREATE INDEX "idx_taste_context_states_profile" ON "taste_context_states"(profile_id);
CREATE INDEX "idx_taste_contexts_profile" ON "taste_contexts"(profile_id);
CREATE INDEX "idx_taste_states_profile" ON "taste_states"(profile_id);
CREATE INDEX "idx_track_preferences_profile" ON "track_preferences"(profile_id);
CREATE INDEX "idx_track_stats_profile" ON "track_stats"(profile_id);
CREATE INDEX "idx_track_tags_profile" ON "track_tags"(profile_id);
CREATE INDEX "idx_training_runs_profile" ON "training_runs"(profile_id);
CREATE INDEX "idx_transition_stats_profile" ON "transition_stats"(profile_id);
CREATE INDEX "idx_transitions_profile" ON "transitions"(profile_id);
CREATE INDEX "idx_user_profile_snapshots_profile" ON "user_profile_snapshots"(profile_id);
DELETE FROM "sqlite_sequence";
INSERT INTO "sqlite_sequence" VALUES('discover_tips',0);
INSERT INTO "sqlite_sequence" VALUES('listening_history',0);
INSERT INTO "sqlite_sequence" VALUES('recommendation_impressions',0);
INSERT INTO "sqlite_sequence" VALUES('user_profile_snapshots',0);
COMMIT;
PRAGMA foreign_keys = ON;
PRAGMA user_version = 8;
CREATE TABLE alembic_version (version_num VARCHAR(32) NOT NULL);
INSERT INTO alembic_version(version_num) VALUES ('musik_8');
`

func Create(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	conn, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Exec(Schema); err != nil {
		return fmt.Errorf("create test database: %w", err)
	}
	return nil
}
