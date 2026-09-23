package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gbschedule/gbschedule/internal/constants"
	"github.com/gbschedule/gbschedule/internal/dto"
	"github.com/gbschedule/gbschedule/internal/model"
	"github.com/gbschedule/gbschedule/internal/repository"
)

// adjustmentSide captures one side of a move/swap at the time the change was
// made: the lesson number occupying it and its full position.
type adjustmentSide struct {
	ScheduleID  uint `json:"schedule_id"`
	Week        uint `json:"week"`
	DayOfWeek   int  `json:"day_of_week"`
	TimeSlotID  uint `json:"time_slot_id"`
	ClassroomID uint `json:"classroom_id"`
}

// adjustmentSnapshot records every side of a move/swap so the change can be
// restored to the original week, day, period and classroom.
type adjustmentSnapshot struct {
	Sides []adjustmentSide `json:"sides"`
}

// findHolder returns the lesson number currently occupying the exact given
// position, or 0 when the position is free. excludeID skips the lesson being
// moved itself.
func (s *scheduleService) findHolder(ctx context.Context, week uint, day int, slotID, classroomID, excludeID uint) (uint, error) {
	items, err := s.schedules.List(ctx, repository.ScheduleFilter{Week: &week})
	if err != nil {
		return 0, fmt.Errorf("scan destination position: %w", err)
	}
	for i := range items {
		item := items[i]
		if item.ID == excludeID {
			continue
		}
		if item.DayOfWeek == day && item.TimeSlotID == slotID && item.ClassroomID == classroomID {
			return item.ID, nil
		}
	}
	return 0, nil
}

// RevertAdjustment undoes a previously completed move or swap.
//
// Before restoring anything it re-checks the target positions against the
// latest timetable and the current teacher unavailable slots: if a target is
// held by another teacher/class/classroom, or falls in a forbidden slot, the
// revert is rejected and both the timetable and the history stay untouched.
// A successful revert is itself recorded, and reverting the same change again
// leaves the timetable unchanged.
func (s *scheduleService) RevertAdjustment(ctx context.Context, logID uint) (*dto.RevertAdjustmentResponse, error) {
	logEntry, err := s.adjustments.GetByID(ctx, logID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get adjustment log: %w", err)
	}
	if logEntry.Action != constants.ActionMove && logEntry.Action != constants.ActionSwap {
		return nil, fmt.Errorf("revert adjustment: %w: only move/swap changes can be reverted, got %q", ErrInvalid, logEntry.Action)
	}
	// Idempotent: a repeated revert must not alter the timetable again.
	if logEntry.RevertedAt != nil {
		return &dto.RevertAdjustmentResponse{AlreadyReverted: true}, nil
	}

	var snapshot adjustmentSnapshot
	if err := json.Unmarshal([]byte(logEntry.Detail), &snapshot); err != nil {
		return nil, fmt.Errorf("revert adjustment: %w: stored snapshot is unreadable", ErrInvalid)
	}
	if len(snapshot.Sides) == 0 {
		return nil, fmt.Errorf("revert adjustment: %w: stored snapshot has no positions", ErrInvalid)
	}

	// Load reference data and the current timetable for validation.
	allSchedules, err := s.schedules.List(ctx, repository.ScheduleFilter{})
	if err != nil {
		return nil, fmt.Errorf("load timetable for revert check: %w", err)
	}
	scheduleMap := make(map[uint]model.Schedule, len(allSchedules))
	for _, item := range allSchedules {
		scheduleMap[item.ID] = item
	}

	slotList, _, err := s.timeSlots.List(ctx, 1, constants.MaxPageSize)
	if err != nil {
		return nil, fmt.Errorf("load time slots for revert check: %w", err)
	}
	slotMap := entityMap(slotList, func(sl model.TimeSlot) uint { return sl.ID })

	teacherIDs := make([]uint, 0, len(allSchedules))
	for _, item := range allSchedules {
		teacherIDs = append(teacherIDs, item.TeacherID)
	}
	teacherEntities, err := s.teachers.GetByIDs(ctx, uniqueIDs(teacherIDs))
	if err != nil {
		return nil, fmt.Errorf("load teachers for revert check: %w", err)
	}
	teacherMap := entityMap(teacherEntities, func(t model.Teacher) uint { return t.ID })

	// Resolve every lesson that must move back to its original position.
	restored := make([]model.Schedule, 0, len(snapshot.Sides))
	for _, side := range snapshot.Sides {
		if side.ScheduleID == 0 {
			// A free destination captured during a move: nothing to restore.
			continue
		}
		item, ok := scheduleMap[side.ScheduleID]
		if !ok {
			return nil, fmt.Errorf("revert adjustment: %w: lesson %d no longer exists", ErrInvalid, side.ScheduleID)
		}
		slot, ok := slotMap[side.TimeSlotID]
		if !ok {
			return nil, fmt.Errorf("revert adjustment: %w: target period %d no longer exists", ErrInvalid, side.TimeSlotID)
		}
		if _, err := s.classrooms.GetByID(ctx, side.ClassroomID); err != nil {
			return nil, fmt.Errorf("revert adjustment: %w: target classroom %d is unavailable", ErrInvalid, side.ClassroomID)
		}
		teacher := teacherMap[item.TeacherID]
		if contains(teacher.UnavailableSlots, slot.Code) {
			return nil, fmt.Errorf("revert adjustment: %w: target period %s is forbidden for teacher %d", ErrConflict, slot.Code, item.TeacherID)
		}
		item.Week = side.Week
		item.DayOfWeek = side.DayOfWeek
		item.TimeSlotID = side.TimeSlotID
		item.ClassroomID = side.ClassroomID
		restored = append(restored, item)
	}
	if len(restored) == 0 {
		return nil, fmt.Errorf("revert adjustment: %w: stored snapshot has no restorable lessons", ErrInvalid)
	}

	// Re-check every target position against the latest timetable. The lessons
	// taking part in this revert are allowed to pass through each other's
	// positions (swap), every other occupant blocks the revert.
	if err := s.validateRevertPositions(restored, scheduleMap); err != nil {
		return nil, err
	}

	revertedAt := time.Now()
	var (
		restoredIDs []uint
		revertLogID uint
	)
	for i := range restored {
		restoredIDs = append(restoredIDs, restored[i].ID)
	}

	err = s.uow.RunInTransaction(ctx, func(schedules repository.ScheduleRepository, adjustments repository.AdjustmentLogRepository) error {
		for i := range restored {
			if err := schedules.Update(ctx, &restored[i]); err != nil {
				return fmt.Errorf("restore schedule %d: %w", restored[i].ID, err)
			}
		}
		detail := map[string]any{
			"reverted_log_id": logID,
			"action":          logEntry.Action,
			"schedule_ids":    restoredIDs,
		}
		id, err := s.recordAdjustment(ctx, adjustments, restoredIDs[0], constants.ActionRevert, detail)
		if err != nil {
			return err
		}
		revertLogID = id
		if err := adjustments.MarkReverted(ctx, logID, revertedAt, revertLogID); err != nil {
			return fmt.Errorf("mark adjustment reverted: %w", err)
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, repository.ErrNotFound):
			return nil, ErrNotFound
		case errors.Is(err, repository.ErrConflict):
			return nil, ErrConflict
		default:
			return nil, mapWriteError("revert adjustment", err)
		}
	}

	responses, err := s.enrichSchedules(ctx, restored)
	if err != nil {
		return nil, err
	}
	conflicts, err := s.CheckConflicts(ctx)
	if err != nil {
		return nil, err
	}
	return &dto.RevertAdjustmentResponse{
		Schedules: responses,
		Conflicts: conflicts,
		LogID:     revertLogID,
	}, nil
}

// validateRevertPositions rejects a restore when a target position is held by
// a different teacher, class or classroom. It mirrors the timetable's own
// teacher/class/classroom collision rules.
func (s *scheduleService) validateRevertPositions(restored []model.Schedule, current map[uint]model.Schedule) error {
	moving := make(map[uint]bool, len(restored))
	for i := range restored {
		moving[restored[i].ID] = true
	}

	for i := range restored {
		target := restored[i]
		for _, other := range current {
			if other.ID == target.ID || moving[other.ID] {
				continue
			}
			if other.Week != target.Week || other.DayOfWeek != target.DayOfWeek || other.TimeSlotID != target.TimeSlotID {
				continue
			}
			switch {
			case other.TeacherID == target.TeacherID:
				return fmt.Errorf("revert adjustment: %w: teacher %d already has a lesson at the target period", ErrConflict, target.TeacherID)
			case other.ClassID == target.ClassID:
				return fmt.Errorf("revert adjustment: %w: class %d already has a lesson at the target period", ErrConflict, target.ClassID)
			case other.ClassroomID == target.ClassroomID:
				return fmt.Errorf("revert adjustment: %w: classroom %d is occupied at the target period", ErrConflict, target.ClassroomID)
			}
		}
	}
	return nil
}

func uniqueIDs(ids []uint) []uint {
	seen := map[uint]bool{}
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id != 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func derefUint(p *uint) uint {
	if p == nil {
		return 0
	}
	return *p
}
