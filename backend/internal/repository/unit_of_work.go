package repository

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// UnitOfWork runs a block of repository calls inside a single database
// transaction so timetable changes and their audit records commit atomically.
type UnitOfWork interface {
	RunInTransaction(ctx context.Context, fn func(schedules ScheduleRepository, adjustments AdjustmentLogRepository) error) error
}

type gormUnitOfWork struct {
	db *gorm.DB
}

// NewUnitOfWork constructs a GORM-backed unit of work.
func NewUnitOfWork(db *gorm.DB) UnitOfWork {
	return &gormUnitOfWork{db: db}
}

func (u *gormUnitOfWork) RunInTransaction(ctx context.Context, fn func(schedules ScheduleRepository, adjustments AdjustmentLogRepository) error) error {
	err := u.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(NewScheduleRepository(tx), NewAdjustmentLogRepository(tx))
	})
	if err != nil {
		return fmt.Errorf("run transaction: %w", err)
	}
	return nil
}
