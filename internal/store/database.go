package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// Explicit transactions and callback transactions both use the Store's GORM
// session. Helpers receive that same transaction, never the base pool.
func (s *Store) begin(ctx context.Context, options *sql.TxOptions) (*gorm.DB, error) {
	tx := s.orm.WithContext(ctx).Begin(options)
	return tx, tx.Error
}

// Preserve the driver's Result for conditional writes while using GORM's
// execution, context and logging path. SQLite result/error semantics remain
// available to callers that distinguish a no-op from a successful mutation.
func execSQL(ctx context.Context, db *gorm.DB, query string, args ...any) (sql.Result, error) {
	result := gorm.WithResult()
	executed := db.WithContext(ctx).Clauses(result).Exec(query, args...)
	if err := executed.Error; err != nil && strings.Contains(err.Error(), "vista_branch_execution_slot") {
		return result.Result, fmt.Errorf("%w: %w", ErrBranchRunning, err)
	}
	return result.Result, executed.Error
}
