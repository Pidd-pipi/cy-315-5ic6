package model

import "gorm.io/gorm"

// AdjustmentLog records a manual timetable change for audit/history purposes.
type AdjustmentLog struct {
	gorm.Model
	ScheduleID uint   `gorm:"index" json:"schedule_id"`
	Action     string `gorm:"size:32;not null" json:"action"`
	Detail     string `gorm:"type:text" json:"detail"`
	// UndoneBy references the undo log that reverted this change.
	// A non-nil value means the change has already been undone.
	UndoneBy *uint `gorm:"index" json:"undone_by"`
}
