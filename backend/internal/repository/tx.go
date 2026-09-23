package repository

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

type txContextKey struct{}

// TransactionManager runs a function inside a database transaction.
type TransactionManager interface {
	WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error
}

// gormTransactionManager is the GORM-backed TransactionManager.
type gormTransactionManager struct {
	db *gorm.DB
}

// NewTransactionManager constructs a GORM-backed transaction manager.
func NewTransactionManager(db *gorm.DB) TransactionManager {
	return &gormTransactionManager{db: db}
}

func (m *gormTransactionManager) WithinTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, txContextKey{}, tx))
	})
}

// withTx returns the transaction carried by ctx, falling back to db when the
// context has none. Repositories use it so the same methods participate in a
// service-level transaction transparently.
func withTx(ctx context.Context, db *gorm.DB) *gorm.DB {
	if tx, ok := ctx.Value(txContextKey{}).(*gorm.DB); ok && tx != nil {
		return tx.WithContext(ctx)
	}
	return db.WithContext(ctx)
}

// RunInTransaction is a convenience helper for repositories or tests that
// already hold a *gorm.DB handle.
func RunInTransaction(db *gorm.DB, fn func(tx *gorm.DB) error) error {
	if err := db.Transaction(fn); err != nil {
		return fmt.Errorf("run in transaction: %w", err)
	}
	return nil
}
