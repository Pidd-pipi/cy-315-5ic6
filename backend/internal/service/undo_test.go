package service_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/gbschedule/gbschedule/internal/constants"
	"github.com/gbschedule/gbschedule/internal/dto"
	"github.com/gbschedule/gbschedule/internal/model"
	"github.com/gbschedule/gbschedule/internal/repository"
	"github.com/gbschedule/gbschedule/internal/service"
)

type undoFixture struct {
	slot1  *model.TimeSlot
	slot2  *model.TimeSlot
	room1  *model.Classroom
	room2  *model.Classroom
	room3  *model.Classroom
	t1     *model.Teacher
	t2     *model.Teacher
	t3     *model.Teacher
	class1 *model.Class
	class2 *model.Class
	course *model.Course
}

func newUndoFixture(t *testing.T, db *gorm.DB, t1Unavailable []string) undoFixture {
	t.Helper()
	f := undoFixture{}
	f.slot1 = &model.TimeSlot{Code: "1", Name: "第一节", StartTime: "08:00", EndTime: "09:40"}
	f.slot2 = &model.TimeSlot{Code: "2", Name: "第二节", StartTime: "10:00", EndTime: "11:40"}
	mustCreate(t, db, f.slot1)
	mustCreate(t, db, f.slot2)
	f.room1 = &model.Classroom{Code: "R301", Name: "301", Capacity: 50}
	f.room2 = &model.Classroom{Code: "R302", Name: "302", Capacity: 50}
	f.room3 = &model.Classroom{Code: "R303", Name: "303", Capacity: 50}
	mustCreate(t, db, f.room1)
	mustCreate(t, db, f.room2)
	mustCreate(t, db, f.room3)
	f.t1 = &model.Teacher{Name: "张老师", EmployeeNo: "T001", UnavailableSlots: t1Unavailable}
	f.t2 = &model.Teacher{Name: "李老师", EmployeeNo: "T002"}
	f.t3 = &model.Teacher{Name: "王老师", EmployeeNo: "T003"}
	mustCreate(t, db, f.t1)
	mustCreate(t, db, f.t2)
	mustCreate(t, db, f.t3)
	f.class1 = &model.Class{Name: "一班", StudentCount: 40, Grade: "高一"}
	f.class2 = &model.Class{Name: "二班", StudentCount: 40, Grade: "高一"}
	mustCreate(t, db, f.class1)
	mustCreate(t, db, f.class2)
	f.course = &model.Course{Name: "数学", Code: "MATH", Duration: 1}
	mustCreate(t, db, f.course)
	return f
}

func mustCreate(t *testing.T, db *gorm.DB, value any) {
	t.Helper()
	if err := db.Create(value).Error; err != nil {
		t.Fatalf("create %T: %v", value, err)
	}
}

func newUndoDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Classroom{}, &model.Teacher{}, &model.Class{}, &model.Course{}, &model.TimeSlot{}, &model.Schedule{}, &model.AdjustmentLog{}); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	return db
}

func newUndoService(db *gorm.DB) service.ScheduleService {
	logger := slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
	return service.NewScheduleService(
		repository.NewScheduleRepository(db),
		repository.NewClassroomRepository(db),
		repository.NewTeacherRepository(db),
		repository.NewClassRepository(db),
		repository.NewCourseRepository(db),
		repository.NewTimeSlotRepository(db),
		repository.NewAdjustmentLogRepository(db),
		repository.NewTransactionManager(db),
		logger,
	)
}

func (f undoFixture) lesson(t *testing.T, db *gorm.DB, week uint, day int, slot, room, teacher, class *uint) *model.Schedule {
	t.Helper()
	item := &model.Schedule{Week: week, DayOfWeek: day, TimeSlotID: *slot, ClassroomID: *room, TeacherID: *teacher, ClassID: *class, CourseID: f.course.ID}
	mustCreate(t, db, item)
	return item
}

// TestUndoMoveRestoresOriginalPosition verifies that undoing a move puts the
// lesson back at its original week/day/period/classroom and writes an undo log.
func TestUndoMoveRestoresOriginalPosition(t *testing.T) {
	ctx := context.Background()
	db := newUndoDB(t)
	f := newUndoFixture(t, db, nil)
	svc := newUndoService(db)

	s1 := f.lesson(t, db, 1, 1, &f.slot1.ID, &f.room1.ID, &f.t1.ID, &f.class1.ID)

	moveResp, err := svc.Move(ctx, &dto.MoveScheduleRequest{
		ScheduleID: s1.ID, Week: 2, DayOfWeek: 2, TimeSlotID: f.slot2.ID, ClassroomID: f.room2.ID,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}

	resp, err := svc.UndoAdjustment(ctx, moveResp.LogID)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if resp.AlreadyUndone {
		t.Fatalf("expected a real undo, got already_undone")
	}
	if resp.LogID == 0 || resp.LogID == moveResp.LogID {
		t.Fatalf("expected a new undo log id, got %d (original %d)", resp.LogID, moveResp.LogID)
	}
	if resp.Action != constants.ActionMove || resp.OriginalLogID != moveResp.LogID {
		t.Fatalf("unexpected undo descriptor: %+v", resp)
	}
	if len(resp.Schedules) != 1 {
		t.Fatalf("expected 1 restored schedule, got %d", len(resp.Schedules))
	}
	got := resp.Schedules[0]
	if got.ID != s1.ID || got.Week != 1 || got.DayOfWeek != 1 || got.TimeSlotID != f.slot1.ID || got.ClassroomID != f.room1.ID {
		t.Fatalf("lesson not restored to original position: %+v", got)
	}

	// The restored position must be immediately queryable through the API.
	queried, err := svc.Get(ctx, s1.ID)
	if err != nil {
		t.Fatalf("get restored schedule: %v", err)
	}
	if queried.Week != 1 || queried.DayOfWeek != 1 || queried.TimeSlotID != f.slot1.ID || queried.ClassroomID != f.room1.ID {
		t.Fatalf("restored position not queryable: %+v", queried)
	}

	// History contains the undo entry and the original move is marked undone.
	logs, total, err := svc.ListAdjustments(ctx, 1, 20)
	if err != nil {
		t.Fatalf("list adjustments: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected 2 history entries, got %d", total)
	}
	var moveLog, undoLog *dto.AdjustmentLogResponse
	for i := range logs {
		switch logs[i].Action {
		case constants.ActionMove:
			moveLog = &logs[i]
		case constants.ActionUndo:
			undoLog = &logs[i]
		}
	}
	if moveLog == nil || moveLog.UndoneBy == nil || *moveLog.UndoneBy != resp.LogID {
		t.Fatalf("move log should point at undo log: %+v", moveLog)
	}
	if undoLog == nil || undoLog.ID != resp.LogID {
		t.Fatalf("undo log missing: %+v", undoLog)
	}
}

// TestUndoMoveIsIdempotent verifies that undoing the same change again never
// touches the timetable and never writes another history entry.
func TestUndoMoveIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newUndoDB(t)
	f := newUndoFixture(t, db, nil)
	svc := newUndoService(db)

	s1 := f.lesson(t, db, 1, 1, &f.slot1.ID, &f.room1.ID, &f.t1.ID, &f.class1.ID)
	moveResp, err := svc.Move(ctx, &dto.MoveScheduleRequest{
		ScheduleID: s1.ID, Week: 2, DayOfWeek: 2, TimeSlotID: f.slot2.ID, ClassroomID: f.room2.ID,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	first, err := svc.UndoAdjustment(ctx, moveResp.LogID)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}

	second, err := svc.UndoAdjustment(ctx, moveResp.LogID)
	if err != nil {
		t.Fatalf("repeat undo: %v", err)
	}
	if !second.AlreadyUndone {
		t.Fatalf("expected already_undone=true on repeat undo")
	}
	if second.LogID != first.LogID {
		t.Fatalf("repeat undo should report the existing undo log id: %d != %d", second.LogID, first.LogID)
	}
	if len(second.Schedules) != 1 || second.Schedules[0].Week != 1 {
		t.Fatalf("repeat undo should still return the restored lesson: %+v", second.Schedules)
	}

	logs, total, err := svc.ListAdjustments(ctx, 1, 20)
	if err != nil {
		t.Fatalf("list adjustments: %v", err)
	}
	if total != 2 {
		t.Fatalf("repeat undo wrote history: total=%d logs=%d", total, len(logs))
	}
}

// TestUndoRejectedWhenTeacherOccupies verifies a blocked undo leaves both the
// timetable and the history exactly as they were before the request.
func TestUndoRejectedWhenTeacherOccupies(t *testing.T) {
	ctx := context.Background()
	db := newUndoDB(t)
	f := newUndoFixture(t, db, nil)
	svc := newUndoService(db)

	s1 := f.lesson(t, db, 1, 1, &f.slot1.ID, &f.room1.ID, &f.t1.ID, &f.class1.ID)
	moveResp, err := svc.Move(ctx, &dto.MoveScheduleRequest{
		ScheduleID: s1.ID, Week: 2, DayOfWeek: 2, TimeSlotID: f.slot2.ID, ClassroomID: f.room2.ID,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}

	// Another teacher now holds the original position (different class/room to
	// isolate the teacher conflict).
	_ = f.lesson(t, db, 1, 1, &f.slot1.ID, &f.room3.ID, &f.t1.ID, &f.class2.ID)

	_, err = svc.UndoAdjustment(ctx, moveResp.LogID)
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	// Timetable frozen at the moved position.
	moved, err := svc.Get(ctx, s1.ID)
	if err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	if moved.Week != 2 || moved.DayOfWeek != 2 || moved.TimeSlotID != f.slot2.ID || moved.ClassroomID != f.room2.ID {
		t.Fatalf("timetable changed on rejected undo: %+v", moved)
	}
	// History unchanged: only the move entry exists and it is not marked undone.
	logs, total, err := svc.ListAdjustments(ctx, 1, 20)
	if err != nil {
		t.Fatalf("list adjustments: %v", err)
	}
	if total != 1 || logs[0].Action != constants.ActionMove || logs[0].UndoneBy != nil {
		t.Fatalf("history changed on rejected undo: total=%d logs=%+v", total, logs)
	}
}

// TestUndoRejectedWhenClassOccupies verifies a class double-booking at the
// original position blocks the undo.
func TestUndoRejectedWhenClassOccupies(t *testing.T) {
	ctx := context.Background()
	db := newUndoDB(t)
	f := newUndoFixture(t, db, nil)
	svc := newUndoService(db)

	s1 := f.lesson(t, db, 1, 1, &f.slot1.ID, &f.room1.ID, &f.t1.ID, &f.class1.ID)
	moveResp, err := svc.Move(ctx, &dto.MoveScheduleRequest{
		ScheduleID: s1.ID, Week: 2, DayOfWeek: 2, TimeSlotID: f.slot2.ID, ClassroomID: f.room2.ID,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	// Same class, different teacher/room now holds the original position.
	_ = f.lesson(t, db, 1, 1, &f.slot1.ID, &f.room3.ID, &f.t2.ID, &f.class1.ID)

	_, err = svc.UndoAdjustment(ctx, moveResp.LogID)
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

// TestUndoRejectedWhenClassroomOccupies verifies a classroom double-booking at
// the original position blocks the undo.
func TestUndoRejectedWhenClassroomOccupies(t *testing.T) {
	ctx := context.Background()
	db := newUndoDB(t)
	f := newUndoFixture(t, db, nil)
	svc := newUndoService(db)

	s1 := f.lesson(t, db, 1, 1, &f.slot1.ID, &f.room1.ID, &f.t1.ID, &f.class1.ID)
	moveResp, err := svc.Move(ctx, &dto.MoveScheduleRequest{
		ScheduleID: s1.ID, Week: 2, DayOfWeek: 2, TimeSlotID: f.slot2.ID, ClassroomID: f.room2.ID,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	// Same room with a different teacher/class now holds the original position.
	_ = f.lesson(t, db, 1, 1, &f.slot1.ID, &f.room1.ID, &f.t2.ID, &f.class2.ID)

	_, err = svc.UndoAdjustment(ctx, moveResp.LogID)
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

// TestUndoRejectedWhenTeacherUnavailable verifies the latest teacher
// unavailable periods are re-checked before restoring.
func TestUndoRejectedWhenTeacherUnavailable(t *testing.T) {
	ctx := context.Background()
	db := newUndoDB(t)
	f := newUndoFixture(t, db, nil)
	svc := newUndoService(db)

	s1 := f.lesson(t, db, 1, 1, &f.slot1.ID, &f.room1.ID, &f.t1.ID, &f.class1.ID)
	moveResp, err := svc.Move(ctx, &dto.MoveScheduleRequest{
		ScheduleID: s1.ID, Week: 2, DayOfWeek: 2, TimeSlotID: f.slot2.ID, ClassroomID: f.room2.ID,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	// The teacher is now forbidden at the original period.
	f.t1.UnavailableSlots = []string{f.slot1.Code}
	if err := db.Save(f.t1).Error; err != nil {
		t.Fatalf("update teacher unavailable slots: %v", err)
	}

	_, err = svc.UndoAdjustment(ctx, moveResp.LogID)
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("expected ErrConflict for teacher unavailable slot, got %v", err)
	}
}

// TestUndoSwapRestoresBothSides verifies both lessons return to their original
// positions after undoing a swap.
func TestUndoSwapRestoresBothSides(t *testing.T) {
	ctx := context.Background()
	db := newUndoDB(t)
	f := newUndoFixture(t, db, nil)
	svc := newUndoService(db)

	s1 := f.lesson(t, db, 1, 1, &f.slot1.ID, &f.room1.ID, &f.t1.ID, &f.class1.ID)
	s2 := f.lesson(t, db, 2, 2, &f.slot2.ID, &f.room2.ID, &f.t2.ID, &f.class2.ID)

	swapResp, err := svc.Swap(ctx, &dto.SwapScheduleRequest{ScheduleAID: s1.ID, ScheduleBID: s2.ID})
	if err != nil {
		t.Fatalf("swap: %v", err)
	}
	if swapResp.LogID == 0 {
		t.Fatal("expected a swap log id")
	}

	resp, err := svc.UndoAdjustment(ctx, swapResp.LogID)
	if err != nil {
		t.Fatalf("undo swap: %v", err)
	}
	if resp.AlreadyUndone {
		t.Fatal("swap undo should not report already_undone")
	}
	if resp.Action != constants.ActionSwap || len(resp.Schedules) != 2 {
		t.Fatalf("unexpected swap undo response: %+v", resp)
	}
	byID := map[uint]dto.ScheduleResponse{}
	for _, item := range resp.Schedules {
		byID[item.ID] = item
	}
	got1 := byID[s1.ID]
	if got1.Week != 1 || got1.DayOfWeek != 1 || got1.TimeSlotID != f.slot1.ID || got1.ClassroomID != f.room1.ID {
		t.Fatalf("schedule a not restored: %+v", got1)
	}
	got2 := byID[s2.ID]
	if got2.Week != 2 || got2.DayOfWeek != 2 || got2.TimeSlotID != f.slot2.ID || got2.ClassroomID != f.room2.ID {
		t.Fatalf("schedule b not restored: %+v", got2)
	}

	// Repeating the swap undo changes nothing.
	again, err := svc.UndoAdjustment(ctx, swapResp.LogID)
	if err != nil {
		t.Fatalf("repeat swap undo: %v", err)
	}
	if !again.AlreadyUndone || again.LogID != resp.LogID {
		t.Fatalf("repeat swap undo should be idempotent: %+v", again)
	}
	logs, total, err := svc.ListAdjustments(ctx, 1, 20)
	if err != nil {
		t.Fatalf("list adjustments: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected swap + undo entries only, got %d: %+v", total, logs)
	}
}

// TestUndoSwapRejectedWhenTargetOccupied verifies a blocked swap undo restores
// neither lesson and writes no history.
func TestUndoSwapRejectedWhenTargetOccupied(t *testing.T) {
	ctx := context.Background()
	db := newUndoDB(t)
	f := newUndoFixture(t, db, nil)
	svc := newUndoService(db)

	s1 := f.lesson(t, db, 1, 1, &f.slot1.ID, &f.room1.ID, &f.t1.ID, &f.class1.ID)
	s2 := f.lesson(t, db, 2, 2, &f.slot2.ID, &f.room2.ID, &f.t2.ID, &f.class2.ID)
	swapResp, err := svc.Swap(ctx, &dto.SwapScheduleRequest{ScheduleAID: s1.ID, ScheduleBID: s2.ID})
	if err != nil {
		t.Fatalf("swap: %v", err)
	}

	// After the swap, s2 sits at s1's old position with room1; a third lesson
	// now also holds that room at the same time, so restoring s1 there would
	// double-book classroom room1.
	_ = f.lesson(t, db, 1, 1, &f.slot1.ID, &f.room1.ID, &f.t3.ID, &f.class2.ID)

	if _, err := svc.UndoAdjustment(ctx, swapResp.LogID); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	got1, err := svc.Get(ctx, s1.ID)
	if err != nil {
		t.Fatalf("get s1: %v", err)
	}
	if got1.Week != 2 || got1.DayOfWeek != 2 || got1.TimeSlotID != f.slot2.ID || got1.ClassroomID != f.room2.ID {
		t.Fatalf("schedule a moved on rejected swap undo: %+v", got1)
	}
	got2, err := svc.Get(ctx, s2.ID)
	if err != nil {
		t.Fatalf("get s2: %v", err)
	}
	if got2.Week != 1 || got2.DayOfWeek != 1 || got2.TimeSlotID != f.slot1.ID || got2.ClassroomID != f.room1.ID {
		t.Fatalf("schedule b moved on rejected swap undo: %+v", got2)
	}
	_, total, err := svc.ListAdjustments(ctx, 1, 20)
	if err != nil {
		t.Fatalf("list adjustments: %v", err)
	}
	if total != 1 {
		t.Fatalf("rejected swap undo wrote history: total=%d", total)
	}
}

// TestUndoRejectsStaleSnapshot verifies a follow-up adjustment invalidates an
// earlier undo.
func TestUndoRejectsStaleSnapshot(t *testing.T) {
	ctx := context.Background()
	db := newUndoDB(t)
	f := newUndoFixture(t, db, nil)
	svc := newUndoService(db)

	s1 := f.lesson(t, db, 1, 1, &f.slot1.ID, &f.room1.ID, &f.t1.ID, &f.class1.ID)
	first, err := svc.Move(ctx, &dto.MoveScheduleRequest{
		ScheduleID: s1.ID, Week: 2, DayOfWeek: 1, TimeSlotID: f.slot1.ID, ClassroomID: f.room2.ID,
	})
	if err != nil {
		t.Fatalf("first move: %v", err)
	}
	if _, err := svc.Move(ctx, &dto.MoveScheduleRequest{
		ScheduleID: s1.ID, Week: 2, DayOfWeek: 2, TimeSlotID: f.slot2.ID, ClassroomID: f.room3.ID,
	}); err != nil {
		t.Fatalf("second move: %v", err)
	}

	_, err = svc.UndoAdjustment(ctx, first.LogID)
	if !errors.Is(err, service.ErrConflict) {
		t.Fatalf("expected ErrConflict for stale snapshot, got %v", err)
	}
}

// TestUndoRejectsInvalidLogs covers undo logs, legacy logs without snapshots
// and unknown log ids.
func TestUndoRejectsInvalidLogs(t *testing.T) {
	ctx := context.Background()
	db := newUndoDB(t)
	f := newUndoFixture(t, db, nil)
	svc := newUndoService(db)

	s1 := f.lesson(t, db, 1, 1, &f.slot1.ID, &f.room1.ID, &f.t1.ID, &f.class1.ID)
	moveResp, err := svc.Move(ctx, &dto.MoveScheduleRequest{
		ScheduleID: s1.ID, Week: 2, DayOfWeek: 2, TimeSlotID: f.slot2.ID, ClassroomID: f.room2.ID,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	undoResp, err := svc.UndoAdjustment(ctx, moveResp.LogID)
	if err != nil {
		t.Fatalf("undo: %v", err)
	}

	// Undoing an undo entry is a bad request.
	if _, err := svc.UndoAdjustment(ctx, undoResp.LogID); !errors.Is(err, service.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for undo-of-undo, got %v", err)
	}

	// Legacy detail without a position snapshot cannot be reverted.
	legacy := &model.AdjustmentLog{
		ScheduleID: s1.ID,
		Action:     constants.ActionMove,
		Detail:     `{"week":2,"day_of_week":2,"time_slot_id":2,"classroom_id":2}`,
	}
	if err := db.Create(legacy).Error; err != nil {
		t.Fatalf("create legacy log: %v", err)
	}
	if _, err := svc.UndoAdjustment(ctx, legacy.ID); !errors.Is(err, service.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for legacy log, got %v", err)
	}

	// Unknown log id is a not-found.
	if _, err := svc.UndoAdjustment(ctx, legacy.ID+9999); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for unknown log, got %v", err)
	}
}
