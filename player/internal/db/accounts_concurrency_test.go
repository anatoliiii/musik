package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestConcurrentPostgresAdminRoleChangeAndDisableKeepsAnActiveAdmin(t *testing.T) {
	databaseURL := os.Getenv("MUSIK_TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Skip("set MUSIK_TEST_POSTGRES_URL to run the isolated PostgreSQL account test")
	}

	schema := "musik_test_" + NewID()
	rootDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rootDB.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatal(err)
	}

	scopedURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := scopedURL.Query()
	query.Set("search_path", schema)
	scopedURL.RawQuery = query.Encode()
	open := func() *Store {
		_, pool, openErr := openPool(scopedURL.String(), "")
		if openErr != nil {
			t.Fatal(openErr)
		}
		pool.SetMaxOpenConns(1)
		store, storeErr := newStore(Postgres, pool)
		if storeErr != nil {
			t.Fatal(storeErr)
		}
		return store
	}
	first, second := open(), open()
	t.Cleanup(func() {
		_ = first.Close()
		_ = second.Close()
		_, _ = rootDB.Exec(`DROP SCHEMA "` + schema + `" CASCADE`)
		_ = rootDB.Close()
	})

	setup := `
CREATE TABLE users (
  id text PRIMARY KEY, status text NOT NULL, display_name text NOT NULL,
  created_at text NOT NULL, updated_at text NOT NULL
);
CREATE TABLE user_roles (
  user_id text NOT NULL REFERENCES users(id), role text NOT NULL,
  PRIMARY KEY(user_id, role)
);
CREATE TABLE profiles (
  id text PRIMARY KEY, owner_user_id text NOT NULL REFERENCES users(id),
  name text NOT NULL, is_default integer NOT NULL, created_at text NOT NULL,
  updated_at text NOT NULL, deleted_at text
);
CREATE TABLE auth_sessions (
  token_hash text PRIMARY KEY, user_id text NOT NULL REFERENCES users(id),
  active_profile_id text NOT NULL, csrf_hash text NOT NULL, created_at text NOT NULL,
  expires_at text NOT NULL, revoked_at text
);
CREATE TABLE device_tokens (
  id text PRIMARY KEY, user_id text NOT NULL, revoked_at text
);
CREATE TABLE installation_state (key text PRIMARY KEY, value text NOT NULL);
INSERT INTO installation_state(key,value) VALUES ('active_admin_guard','1');
CREATE FUNCTION pause_admin_role_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.role = 'admin' THEN PERFORM pg_sleep(0.75); END IF;
  RETURN OLD;
END;
$$;
CREATE TRIGGER pause_admin_role_delete BEFORE DELETE ON user_roles
  FOR EACH ROW EXECUTE FUNCTION pause_admin_role_delete();
CREATE FUNCTION pause_admin_disable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_sleep(0.75);
  RETURN NEW;
END;
$$;
CREATE TRIGGER pause_admin_disable BEFORE UPDATE OF status ON users
  FOR EACH ROW WHEN (NEW.status = 'disabled') EXECUTE FUNCTION pause_admin_disable();
`
	if _, err := first.DB.Exec(setup); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	adminA, adminB := NewID(), NewID()
	for _, id := range []string{adminA, adminB} {
		if _, err := first.DB.Exec(
			`INSERT INTO users(id, status, display_name, created_at, updated_at) VALUES (?, 'active', ?, ?, ?)`,
			id, id, now, now,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := first.DB.Exec(`INSERT INTO user_roles(user_id, role) VALUES (?, 'admin')`, id); err != nil {
			t.Fatal(err)
		}
	}

	disable := false
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; results <- first.UpdateAccount(context.Background(), adminA, adminA, "", &disable) }()
	go func() {
		<-start
		results <- second.UpdateAccount(context.Background(), adminB, adminB, "disabled", nil)
	}()
	close(start)
	succeeded, rejected := 0, 0
	for range 2 {
		select {
		case err := <-results:
			if err == nil {
				succeeded++
			} else if err == ErrLastAdmin {
				rejected++
			} else {
				t.Fatalf("concurrent account update failed unexpectedly: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent PostgreSQL account updates did not finish")
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent admin changes: succeeded=%d rejected_as_last_admin=%d, want one each", succeeded, rejected)
	}

	var activeAdmins int
	if err := first.DB.Get(context.Background(), &activeAdmins, `
SELECT COUNT(*) FROM users JOIN user_roles ON user_roles.user_id = users.id
WHERE users.status = 'active' AND user_roles.role = 'admin'`); err != nil {
		t.Fatal(err)
	}
	if activeAdmins < 1 {
		t.Fatal(fmt.Sprintf("concurrent PostgreSQL updates left %d active administrators", activeAdmins))
	}
}
