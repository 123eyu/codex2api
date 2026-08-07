package database

import (
	"context"
	"strings"
)

// CompareAndSwapPromptFilterReviewAPIKeys replaces only the review key field
// when it still matches the value reviewed by the caller. This prevents an
// individual deletion from overwriting a concurrent settings save.
func (db *DB) CompareAndSwapPromptFilterReviewAPIKeys(ctx context.Context, expected, replacement string) (bool, error) {
	expected = strings.TrimSpace(expected)
	replacement = strings.TrimSpace(replacement)
	var swapped bool
	err := db.withSQLiteWriteLock(ctx, func() error {
		result, err := db.conn.ExecContext(ctx, `
			UPDATE system_settings SET prompt_filter_review_api_key=$1
			WHERE id=1 AND TRIM(COALESCE(prompt_filter_review_api_key, ''))=$2
		`, replacement, expected)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		swapped = affected == 1
		return nil
	})
	return swapped, err
}
