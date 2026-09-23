package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gbschedule/gbschedule/internal/constants"
	"github.com/gbschedule/gbschedule/internal/dto"
	"github.com/gbschedule/gbschedule/internal/model"
	"github.com/gbschedule/gbschedule/internal/repository"
)

// adjustmentPosition is the full location of one lesson: week, weekday,
// period (time slot) and classroom.
type adjustmentPosition struct {
	Week        uint `json:"week"`
	DayOfWeek   int  `json:"day_of_week"`
	TimeSlotID  uint `json:"time_slot_id"`
	ClassroomID uint `json:"classroom_id"`
}

func adjustmentPositionFromSchedule(item model.Schedule) adjustmentPosition {
	return adjustmentPosition{
		Week:        item.Week,
		DayOfWeek:   item.DayOfWeek,
		TimeSlotID:  item.TimeSlotID,
		ClassroomID: item.ClassroomID,
	}
}

func (p adjustmentPosition) equals(item model.Schedule) bool {
	return p.Week == item.Week && p.DayOfWeek == item.DayOfWeek &&
		p.TimeSlotID == item.TimeSlotID && p.ClassroomID == item.ClassroomID
}

func (p adjustmentPosition) apply(item *model.Schedule) {
	item.Week = p.Week
	item.DayOfWeek = p.DayOfWeek
	item.TimeSlotID = p.TimeSlotID
	item.ClassroomID = p.ClassroomID
}

// adjustmentSide records one lesson's position before and after a change.
type adjustmentSide struct {
	ScheduleID uint               `json:"schedule_id"`
	Before     adjustmentPosition `json:"before"`
	After      adjustmentPosition `json:"after"`
}

// moveAdjustmentDetail is the detail payload stored for move actions.
type moveAdjustmentDetail struct {
	ScheduleID uint               `json:"schedule_id"`
	Before     adjustmentPosition `json:"before"`
	After      adjustmentPosition `json:"after"`
}

// swapAdjustmentDetail is the detail payload stored for swap actions. Both
// sides keep their lesson number and complete positions on both ends.
type swapAdjustmentDetail struct {
	Sides []adjustmentSide `json:"sides"`
}

// undoAdjustmentDetail is the detail payload of an undo history entry.
type undoAdjustmentDetail struct {
	OriginalLogID uint             `json:"original_log_id"`
	Action        string           `json:"action"`
	Sides         []adjustmentSide `json:"sides"`
}

// UndoAdjustment reverts a previous move or swap. The restoration is
// re-validated against the current timetable and teacher unavailable periods;
// if any target position is occupied by another teacher/class/classroom (or
// forbidden by the teacher), the timetable and history stay untouched.
func (s *scheduleService) UndoAdjustment(ctx context.Context, adjustmentLogID uint) (*dto.UndoAdjustmentResponse, error) {
	// Fast path outside the transaction: repeat undo must never write.
	logEntry, err := s.adjustments.GetByID(ctx, adjustmentLogID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get adjustment log: %w", err)
	}
	if logEntry.Action == constants.ActionUndo {
		return nil, fmt.Errorf("undo adjustment: %w: log %d is itself an undo entry", ErrInvalid, adjustmentLogID)
	}
	if logEntry.UndoneBy != nil {
		return s.buildAlreadyUndoneResponse(ctx, logEntry)
	}

	sides, action, err := parseAdjustmentDetail(logEntry.Action, logEntry.Detail)
	if err != nil {
		return nil, err
	}

	var undoLogID uint
	err = s.tx.WithinTransaction(ctx, func(txCtx context.Context) error {
		// Re-read inside the transaction to close the race with a concurrent undo.
		currentLog, err := s.adjustments.GetByID(txCtx, adjustmentLogID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return ErrNotFound
			}
			return fmt.Errorf("reload adjustment log: %w", err)
		}
		if currentLog.UndoneBy != nil {
			return errAdjustmentAlreadyUndone
		}

		targets, err := s.loadUndoTargets(txCtx, sides)
		if err != nil {
			return err
		}

		allSchedules, err := s.schedules.List(txCtx, repository.ScheduleFilter{})
		if err != nil {
			return err
		}
		if err := s.validateUndoPositions(txCtx, allSchedules, targets); err != nil {
			return err
		}

		// All checks passed: restore the original positions.
		for i := range targets {
			targets[i].Side.Before.apply(targets[i].Schedule)
			if err := s.schedules.Update(txCtx, targets[i].Schedule); err != nil {
				return fmt.Errorf("restore schedule %d: %w", targets[i].Schedule.ID, err)
			}
		}

		undoLog := &model.AdjustmentLog{
			ScheduleID: logEntry.ScheduleID,
			Action:     constants.ActionUndo,
			Detail:     mustMarshalUndoDetail(adjustmentLogID, action, sides),
		}
		if err := s.adjustments.Create(txCtx, undoLog); err != nil {
			return fmt.Errorf("record undo adjustment: %w", err)
		}
		if err := s.adjustments.MarkUndone(txCtx, adjustmentLogID, undoLog.ID); err != nil {
			if errors.Is(err, repository.ErrAdjustmentAlreadyUndone) {
				return errAdjustmentAlreadyUndone
			}
			return fmt.Errorf("mark adjustment undone: %w", err)
		}
		undoLogID = undoLog.ID
		return nil
	})
	if err != nil {
		if errors.Is(err, errAdjustmentAlreadyUndone) {
			return s.buildAlreadyUndoneResponse(ctx, logEntry)
		}
		return nil, err
	}

	restored, err := s.loadRestoredSchedules(ctx, sides)
	if err != nil {
		return nil, err
	}
	conflicts, err := s.CheckConflicts(ctx)
	if err != nil {
		return nil, err
	}
	return &dto.UndoAdjustmentResponse{
		LogID:         undoLogID,
		OriginalLogID: adjustmentLogID,
		Action:        action,
		Schedules:     restored,
		Conflicts:     conflicts,
	}, nil
}

// undoTarget pairs a recorded change side with the lesson as it currently is.
type undoTarget struct {
	Side     adjustmentSide
	Schedule *model.Schedule
}

func (s *scheduleService) loadUndoTargets(ctx context.Context, sides []adjustmentSide) ([]undoTarget, error) {
	targets := make([]undoTarget, 0, len(sides))
	for _, side := range sides {
		item, err := s.schedules.GetByID(ctx, side.ScheduleID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, fmt.Errorf("undo adjustment: %w: schedule %d no longer exists", ErrInvalid, side.ScheduleID)
			}
			return nil, fmt.Errorf("load schedule %d: %w", side.ScheduleID, err)
		}
		// The lesson must still sit at the position the change put it in;
		// otherwise the stored snapshot cannot describe how to revert safely.
		if !side.After.equals(*item) {
			return nil, fmt.Errorf("undo adjustment: %w: schedule %d has changed since the recorded adjustment", ErrConflict, side.ScheduleID)
		}
		targets = append(targets, undoTarget{Side: side, Schedule: item})
	}
	return targets, nil
}

// validateUndoPositions simulates the timetable after restoration and rejects
// any hard conflict: teacher/class/classroom double booking at the same
// week/day/period, a teacher unavailable period, or a missing time slot or
// classroom. Capacity problems stay soft and are reported after restoration.
func (s *scheduleService) validateUndoPositions(ctx context.Context, all []model.Schedule, targets []undoTarget) error {
	slots, _, err := s.timeSlots.List(ctx, 1, constants.MaxPageSize)
	if err != nil {
		return fmt.Errorf("load time slots: %w", err)
	}
	slotMap := make(map[uint]model.TimeSlot, len(slots))
	for i := range slots {
		slotMap[slots[i].ID] = slots[i]
	}
	restoring := make(map[uint]bool, len(targets))
	restoredSchedules := make([]model.Schedule, 0, len(targets))
	for i := range targets {
		restoring[targets[i].Schedule.ID] = true
		restored := *targets[i].Schedule
		targets[i].Side.Before.apply(&restored)
		restoredSchedules = append(restoredSchedules, restored)
	}

	// Load every referenced classroom up front, including those referenced by
	// the prospective (restored) positions, so a move involving the only
	// lesson using that classroom still validates.
	classrooms, err := s.classrooms.GetByIDs(ctx, uniqueClassroomIDs(append(append([]model.Schedule{}, all...), restoredSchedules...)))
	if err != nil {
		return fmt.Errorf("load classrooms: %w", err)
	}
	classroomSet := make(map[uint]bool, len(classrooms))
	for i := range classrooms {
		classroomSet[classrooms[i].ID] = true
	}
	teachers, err := s.teachers.GetByIDs(ctx, uniqueTeacherIDs(all))
	if err != nil {
		return fmt.Errorf("load teachers: %w", err)
	}
	teacherMap := make(map[uint]model.Teacher, len(teachers))
	for i := range teachers {
		teacherMap[teachers[i].ID] = teachers[i]
	}

	// Build the prospective timetable: unchanged lessons plus restored lessons
	// placed back at their original positions.
	prospective := make([]model.Schedule, 0, len(all))
	restoredByID := make(map[uint]model.Schedule, len(targets))
	for _, item := range all {
		if restoring[item.ID] {
			continue
		}
		prospective = append(prospective, item)
	}
	for _, restored := range restoredSchedules {
		prospective = append(prospective, restored)
		restoredByID[restored.ID] = restored
	}

	teacherSlots := map[string]uint{}
	classSlots := map[string]uint{}
	classroomSlots := map[string]uint{}
	for _, item := range prospective {
		slotKey := fmt.Sprintf("%d-%d-%d", item.Week, item.DayOfWeek, item.TimeSlotID)
		tKey := slotKey + "-t-" + fmt.Sprint(item.TeacherID)
		cKey := slotKey + "-c-" + fmt.Sprint(item.ClassID)
		rKey := slotKey + "-r-" + fmt.Sprint(item.ClassroomID)
		if existing, ok := teacherSlots[tKey]; ok && existing != item.ID {
			return fmt.Errorf("undo adjustment: %w: week %d day %d slot %d is already taken by teacher %d (schedule %d)", ErrConflict, item.Week, item.DayOfWeek, item.TimeSlotID, item.TeacherID, existing)
		}
		if existing, ok := classSlots[cKey]; ok && existing != item.ID {
			return fmt.Errorf("undo adjustment: %w: week %d day %d slot %d is already taken by class %d (schedule %d)", ErrConflict, item.Week, item.DayOfWeek, item.TimeSlotID, item.ClassID, existing)
		}
		if existing, ok := classroomSlots[rKey]; ok && existing != item.ID {
			return fmt.Errorf("undo adjustment: %w: week %d day %d slot %d is already taken by classroom %d (schedule %d)", ErrConflict, item.Week, item.DayOfWeek, item.TimeSlotID, item.ClassroomID, existing)
		}
		teacherSlots[tKey] = item.ID
		classSlots[cKey] = item.ID
		classroomSlots[rKey] = item.ID
	}

	// Reference checks only apply to the restored lessons: the rest of the
	// timetable is unchanged and was already serving the API.
	for i := range targets {
		restored := restoredByID[targets[i].Schedule.ID]
		slot, ok := slotMap[restored.TimeSlotID]
		if !ok {
			return fmt.Errorf("undo adjustment: %w: time slot %d no longer exists", ErrConflict, restored.TimeSlotID)
		}
		if !classroomSet[restored.ClassroomID] {
			return fmt.Errorf("undo adjustment: %w: classroom %d no longer exists", ErrConflict, restored.ClassroomID)
		}
		if teacher, ok := teacherMap[restored.TeacherID]; ok && contains(teacher.UnavailableSlots, slot.Code) {
			return fmt.Errorf("undo adjustment: %w: slot %s is in teacher %d unavailable periods", ErrConflict, slot.Code, restored.TeacherID)
		}
	}
	return nil
}

func (s *scheduleService) loadRestoredSchedules(ctx context.Context, sides []adjustmentSide) ([]dto.ScheduleResponse, error) {
	items := make([]model.Schedule, 0, len(sides))
	for _, side := range sides {
		item, err := s.schedules.GetByID(ctx, side.ScheduleID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("load restored schedule %d: %w", side.ScheduleID, err)
		}
		items = append(items, *item)
	}
	return s.enrichSchedules(ctx, items)
}

// buildAlreadyUndoneResponse serves repeat undo requests: no timetable change,
// no new history entry, while returning the current state of the lessons.
func (s *scheduleService) buildAlreadyUndoneResponse(ctx context.Context, logEntry *model.AdjustmentLog) (*dto.UndoAdjustmentResponse, error) {
	sides, action, err := parseAdjustmentDetail(logEntry.Action, logEntry.Detail)
	if err != nil {
		return nil, err
	}
	restored, err := s.loadRestoredSchedules(ctx, sides)
	if err != nil {
		return nil, err
	}
	conflicts, err := s.CheckConflicts(ctx)
	if err != nil {
		return nil, err
	}
	resp := &dto.UndoAdjustmentResponse{
		OriginalLogID: logEntry.ID,
		Action:        action,
		Schedules:     restored,
		Conflicts:     conflicts,
		AlreadyUndone: true,
	}
	if logEntry.UndoneBy != nil {
		resp.LogID = *logEntry.UndoneBy
	}
	return resp, nil
}

func parseAdjustmentDetail(action, raw string) ([]adjustmentSide, string, error) {
	switch action {
	case constants.ActionMove:
		var detail moveAdjustmentDetail
		if err := json.Unmarshal([]byte(raw), &detail); err != nil {
			return nil, "", fmt.Errorf("undo adjustment: %w: unreadable move detail: %v", ErrInvalid, err)
		}
		if detail.ScheduleID == 0 || isZeroPosition(detail.Before) {
			return nil, "", fmt.Errorf("undo adjustment: %w: move log %d has no restorable snapshot", ErrInvalid, detail.ScheduleID)
		}
		return []adjustmentSide{{ScheduleID: detail.ScheduleID, Before: detail.Before, After: detail.After}}, action, nil
	case constants.ActionSwap:
		var detail swapAdjustmentDetail
		if err := json.Unmarshal([]byte(raw), &detail); err != nil {
			return nil, "", fmt.Errorf("undo adjustment: %w: unreadable swap detail: %v", ErrInvalid, err)
		}
		if len(detail.Sides) < 2 {
			return nil, "", fmt.Errorf("undo adjustment: %w: swap log has incomplete snapshots", ErrInvalid)
		}
		for _, side := range detail.Sides {
			if side.ScheduleID == 0 || isZeroPosition(side.Before) {
				return nil, "", fmt.Errorf("undo adjustment: %w: swap log has incomplete snapshots", ErrInvalid)
			}
		}
		return detail.Sides, action, nil
	default:
		return nil, "", fmt.Errorf("undo adjustment: %w: unsupported action %q", ErrInvalid, action)
	}
}

func isZeroPosition(p adjustmentPosition) bool {
	return p.TimeSlotID == 0 || p.ClassroomID == 0 || p.Week == 0 || p.DayOfWeek == 0
}

func mustMarshalUndoDetail(originalLogID uint, action string, sides []adjustmentSide) string {
	data, err := json.Marshal(undoAdjustmentDetail{OriginalLogID: originalLogID, Action: action, Sides: sides})
	if err != nil {
		// All fields are plain values; marshalling cannot realistically fail.
		return fmt.Sprintf(`{"original_log_id":%d}`, originalLogID)
	}
	return string(data)
}
