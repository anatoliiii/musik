package playlist

import (
	"context"
	"errors"

	"github.com/torwin-job/musik/player/internal/db"
)

type ruleError struct{ error }

func (e ruleError) Unwrap() error { return e.error }

// IsRuleError reports a smart-rule parse or evaluation failure.
func IsRuleError(err error) bool {
	var re ruleError
	return errors.As(err, &re)
}

func ruleItems(ctx context.Context, store *db.Store, ruleJSON string) ([]db.UserPlaylistItem, error) {
	rule, err := ParseRule(ruleJSON)
	if err != nil {
		return nil, ruleError{err}
	}
	ids, err := Evaluate(ctx, store, rule)
	if err != nil {
		return nil, ruleError{err}
	}
	items := make([]db.UserPlaylistItem, 0, len(ids))
	for _, trackID := range ids {
		items = append(items, db.UserPlaylistItem{TrackID: trackID, Source: "rule"})
	}
	return items, nil
}

// CreateSmart evaluates the rule and inserts the playlist with its tracks
// in one transaction.
func CreateSmart(ctx context.Context, store *db.Store, pl db.UserPlaylist) (*db.UserPlaylist, error) {
	items, err := ruleItems(ctx, store, pl.RuleJSON)
	if err != nil {
		return nil, err
	}
	pl.RuleSchema = RuleSchemaVersion
	created, err := store.CreateUserPlaylistWithItems(ctx, pl, items)
	if err != nil {
		return nil, err
	}
	return loadPlaylist(store, created.ID)
}

// UpdateSmart evaluates the rule and replaces the playlist tracks in one
// transaction.
func UpdateSmart(ctx context.Context, store *db.Store, pl db.UserPlaylist) (*db.UserPlaylist, error) {
	items, err := ruleItems(ctx, store, pl.RuleJSON)
	if err != nil {
		return nil, err
	}
	pl.RuleSchema = RuleSchemaVersion
	if err := store.UpdateUserPlaylistWithItems(ctx, pl, items); err != nil {
		return nil, err
	}
	return loadPlaylist(store, pl.ID)
}

// Materialize replaces a smart playlist's tracks from its current rule.
func Materialize(ctx context.Context, store *db.Store, id int64, ruleJSON string) error {
	items, err := ruleItems(ctx, store, ruleJSON)
	if err != nil {
		return err
	}
	return store.ReplacePlaylistItems(ctx, id, items)
}

func loadPlaylist(store *db.Store, id int64) (*db.UserPlaylist, error) {
	out, err := store.GetUserPlaylist(id)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, db.ErrPlaylistNotFound
	}
	return out, nil
}
