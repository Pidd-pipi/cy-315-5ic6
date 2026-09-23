package model

import (
	"time"

	"gorm.io/gorm"
)

// AdjustmentLog records a manual timetable change for audit/history purposes.
type AdjustmentLog struct {
	gorm.Model
	ScheduleID uint   `gorm:"index" json:"schedule_id"`
	Action     string `gorm:"size:32;not null" json:"action"`
	Detail     string `gorm:"type:text" json:"detail"`
	// RevertedAt is set once this change has been undone; nil means it can
	// still be reverted. It also makes a revert idempotent.
	RevertedAt *time.Time `gorm:"index" json:"reverted_at"`
	// RevertLogID points at the adjustment log recorded for the undo itself.
	RevertLogID *uint `gorm:"index" json:"revert_log_id"`
}
