package repository

import (
	"context"
	"fmt"

	"github.com/gbschedule/gbschedule/internal/model"
	"gorm.io/gorm"
)

// AdjustmentLogRepository persists manual adjustment history.
type AdjustmentLogRepository interface {
	Create(ctx context.Context, log *model.AdjustmentLog) error
	GetByID(ctx context.Context, id uint) (*model.AdjustmentLog, error)
	List(ctx context.Context, page, pageSize int) ([]model.AdjustmentLog, int64, error)
	// MarkUndone records the undo log id on the original change. It only
	// succeeds when the change has not been undone yet.
	MarkUndone(ctx context.Context, id, undoLogID uint) error
}

type adjustmentLogRepository struct {
	db *gorm.DB
}

// NewAdjustmentLogRepository constructs an adjustment log repository.
func NewAdjustmentLogRepository(db *gorm.DB) AdjustmentLogRepository {
	return &adjustmentLogRepository{db: db}
}

func (r *adjustmentLogRepository) Create(ctx context.Context, log *model.AdjustmentLog) error {
	if err := withTx(ctx, r.db).Create(log).Error; err != nil {
		return fmt.Errorf("create adjustment log: %w", err)
	}
	return nil
}

func (r *adjustmentLogRepository) GetByID(ctx context.Context, id uint) (*model.AdjustmentLog, error) {
	var item model.AdjustmentLog
	if err := withTx(ctx, r.db).First(&item, id).Error; err != nil {
		return nil, normalizeError(err)
	}
	return &item, nil
}

func (r *adjustmentLogRepository) List(ctx context.Context, page, pageSize int) ([]model.AdjustmentLog, int64, error) {
	var items []model.AdjustmentLog
	var total int64
	db := withTx(ctx, r.db)
	if err := db.Model(&model.AdjustmentLog{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count adjustment logs: %w", err)
	}
	if err := paginate(db.Model(&model.AdjustmentLog{}), page, pageSize).
		Order("id DESC").Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("list adjustment logs: %w", err)
	}
	return items, total, nil
}

func (r *adjustmentLogRepository) MarkUndone(ctx context.Context, id, undoLogID uint) error {
	result := withTx(ctx, r.db).Model(&model.AdjustmentLog{}).
		Where("id = ? AND undone_by IS NULL", id).
		Update("undone_by", undoLogID)
	if result.Error != nil {
		return fmt.Errorf("mark adjustment undone: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrAdjustmentAlreadyUndone
	}
	return nil
}
