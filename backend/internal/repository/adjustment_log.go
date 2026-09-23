package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/gbschedule/gbschedule/internal/model"
	"gorm.io/gorm"
)

// AdjustmentLogRepository persists manual adjustment history.
type AdjustmentLogRepository interface {
	Create(ctx context.Context, log *model.AdjustmentLog) error
	GetByID(ctx context.Context, id uint) (*model.AdjustmentLog, error)
	List(ctx context.Context, page, pageSize int) ([]model.AdjustmentLog, int64, error)
	// MarkReverted stamps an adjustment as undone exactly once. It returns
	// ErrNotFound when the log does not exist and ErrConflict when another
	// transaction already reverted it.
	MarkReverted(ctx context.Context, id uint, revertedAt time.Time, revertLogID uint) error
}

type adjustmentLogRepository struct {
	db *gorm.DB
}

// NewAdjustmentLogRepository constructs an adjustment log repository.
func NewAdjustmentLogRepository(db *gorm.DB) AdjustmentLogRepository {
	return &adjustmentLogRepository{db: db}
}

func (r *adjustmentLogRepository) Create(ctx context.Context, log *model.AdjustmentLog) error {
	if err := r.db.WithContext(ctx).Create(log).Error; err != nil {
		return fmt.Errorf("create adjustment log: %w", err)
	}
	return nil
}

func (r *adjustmentLogRepository) GetByID(ctx context.Context, id uint) (*model.AdjustmentLog, error) {
	var item model.AdjustmentLog
	if err := r.db.WithContext(ctx).First(&item, id).Error; err != nil {
		return nil, normalizeError(err)
	}
	return &item, nil
}

func (r *adjustmentLogRepository) MarkReverted(ctx context.Context, id uint, revertedAt time.Time, revertLogID uint) error {
	result := r.db.WithContext(ctx).Model(&model.AdjustmentLog{}).
		Where("id = ? AND reverted_at IS NULL", id).
		Updates(map[string]any{"reverted_at": revertedAt, "revert_log_id": revertLogID})
	if result.Error != nil {
		return fmt.Errorf("mark adjustment reverted: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		// Distinguish a missing log from one that was already reverted.
		var count int64
		if err := r.db.WithContext(ctx).Model(&model.AdjustmentLog{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return fmt.Errorf("check adjustment existence: %w", err)
		}
		if count == 0 {
			return ErrNotFound
		}
		return ErrConflict
	}
	return nil
}

func (r *adjustmentLogRepository) List(ctx context.Context, page, pageSize int) ([]model.AdjustmentLog, int64, error) {
	var items []model.AdjustmentLog
	var total int64
	if err := r.db.WithContext(ctx).Model(&model.AdjustmentLog{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count adjustment logs: %w", err)
	}
	if err := paginate(r.db.WithContext(ctx).Model(&model.AdjustmentLog{}), page, pageSize).
		Order("id DESC").Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("list adjustment logs: %w", err)
	}
	return items, total, nil
}
