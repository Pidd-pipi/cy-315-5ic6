package service_test

import (
	"context"
	"testing"

	"gorm.io/gorm"

	"github.com/gbschedule/gbschedule/internal/constants"
	"github.com/gbschedule/gbschedule/internal/dto"
	"github.com/gbschedule/gbschedule/internal/model"
)

type revertFixture struct {
	slot1, slot2       *model.TimeSlot
	room1, room2       *model.Classroom
	teacher1, teacher2 *model.Teacher
	class1, class2     *model.Class
	course             *model.Course
}

// seedRevertFixture creates two slots, two rooms, two teachers, two classes
// and one course for revert tests.
func seedRevertFixture(t *testing.T, db *gorm.DB) revertFixture {
	t.Helper()
	slot1 := &model.TimeSlot{Code: "1", Name: "第一节", StartTime: "08:00", EndTime: "09:40"}
	slot2 := &model.TimeSlot{Code: "2", Name: "第二节", StartTime: "10:00", EndTime: "11:40"}
	mustCreate(t, db, slot1)
	mustCreate(t, db, slot2)

	room1 := &model.Classroom{Code: "R301", Name: "301教室", Capacity: 50}
	room2 := &model.Classroom{Code: "R302", Name: "302教室", Capacity: 50}
	mustCreate(t, db, room1)
	mustCreate(t, db, room2)

	teacher1 := &model.Teacher{Name: "张老师", EmployeeNo: "T001", Subjects: []string{"数学"}}
	teacher2 := &model.Teacher{Name: "李老师", EmployeeNo: "T002", Subjects: []string{"语文"}}
	mustCreate(t, db, teacher1)
	mustCreate(t, db, teacher2)

	class1 := &model.Class{Name: "一班", StudentCount: 40, Grade: "高一"}
	class2 := &model.Class{Name: "二班", StudentCount: 40, Grade: "高一"}
	mustCreate(t, db, class1)
	mustCreate(t, db, class2)

	course := &model.Course{Name: "数学", Code: "MATH", Duration: 1}
	mustCreate(t, db, course)

	return revertFixture{slot1, slot2, room1, room2, teacher1, teacher2, class1, class2, course}
}

func mustCreate(t *testing.T, db *gorm.DB, value any) {
	t.Helper()
	if err := db.Create(value).Error; err != nil {
		t.Fatalf("create %T: %v", value, err)
	}
}

// TestRevertMoveRestoresOriginalPosition verifies a move is recorded with both
// sides and undoing it restores the original week/day/period/classroom.
func TestRevertMoveRestoresOriginalPosition(t *testing.T) {
	ctx := context.Background()
	db := newScheduleTestDB(t)
	f := seedRevertFixture(t, db)

	original := &model.Schedule{Week: 1, DayOfWeek: 1, TimeSlotID: f.slot1.ID, ClassroomID: f.room1.ID, TeacherID: f.teacher1.ID, ClassID: f.class1.ID, CourseID: f.course.ID}
	mustCreate(t, db, original)

	svc := newScheduleService(t, db)
	moveResp, err := svc.Move(ctx, &dto.MoveScheduleRequest{
		ScheduleID: original.ID, Week: 2, DayOfWeek: 3, TimeSlotID: f.slot2.ID, ClassroomID: f.room2.ID,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}

	// The new position is immediately queryable after the move.
	moved, err := svc.Get(ctx, original.ID)
	if err != nil {
		t.Fatalf("get moved: %v", err)
	}
	if moved.Week != 2 || moved.DayOfWeek != 3 || moved.TimeSlotID != f.slot2.ID || moved.ClassroomID != f.room2.ID {
		t.Fatalf("move did not apply: %+v", moved)
	}

	revertResp, err := svc.RevertAdjustment(ctx, moveResp.LogID)
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if revertResp.AlreadyReverted {
		t.Fatal("first revert must not report already_reverted")
	}
	if len(revertResp.Schedules) != 1 {
		t.Fatalf("expected 1 restored schedule, got %d", len(revertResp.Schedules))
	}
	got := revertResp.Schedules[0]
	if got.Week != 1 || got.DayOfWeek != 1 || got.TimeSlotID != f.slot1.ID || got.ClassroomID != f.room1.ID {
		t.Fatalf("revert did not restore original position: %+v", got)
	}

	// The restored position is immediately queryable.
	restored, err := svc.Get(ctx, original.ID)
	if err != nil {
		t.Fatalf("get restored: %v", err)
	}
	if restored.Week != 1 || restored.DayOfWeek != 1 || restored.TimeSlotID != f.slot1.ID || restored.ClassroomID != f.room1.ID {
		t.Fatalf("restored schedule not queryable at original position: %+v", restored)
	}

	// The undo itself is recorded and linked back to the move log.
	logs, total, err := svc.ListAdjustments(ctx, 1, 50)
	if err != nil {
		t.Fatalf("list adjustments: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected move + revert history entries, got %d", total)
	}
	var revertLog, moveLog *dto.AdjustmentLogResponse
	for i := range logs {
		switch logs[i].Action {
		case constants.ActionRevert:
			revertLog = &logs[i]
		case constants.ActionMove:
			moveLog = &logs[i]
		}
	}
	if revertLog == nil || moveLog == nil {
		t.Fatalf("missing move/revert history: %+v", logs)
	}
	if !moveLog.Reverted || moveLog.RevertLogID != revertLog.ID {
		t.Fatalf("move log not linked to revert: %+v", moveLog)
	}
}

// TestRevertMoveIdempotent verifies reverting the same change again neither
// touches the timetable nor appends another history record.
func TestRevertMoveIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newScheduleTestDB(t)
	f := seedRevertFixture(t, db)

	original := &model.Schedule{Week: 1, DayOfWeek: 1, TimeSlotID: f.slot1.ID, ClassroomID: f.room1.ID, TeacherID: f.teacher1.ID, ClassID: f.class1.ID, CourseID: f.course.ID}
	mustCreate(t, db, original)
	svc := newScheduleService(t, db)

	moveResp, err := svc.Move(ctx, &dto.MoveScheduleRequest{
		ScheduleID: original.ID, Week: 2, DayOfWeek: 3, TimeSlotID: f.slot2.ID, ClassroomID: f.room2.ID,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := svc.RevertAdjustment(ctx, moveResp.LogID); err != nil {
		t.Fatalf("first revert: %v", err)
	}

	again, err := svc.RevertAdjustment(ctx, moveResp.LogID)
	if err != nil {
		t.Fatalf("second revert should be idempotent, got error: %v", err)
	}
	if !again.AlreadyReverted {
		t.Fatal("second revert must report already_reverted")
	}
	if len(again.Schedules) != 0 || again.LogID != 0 {
		t.Fatal("idempotent revert must not restore schedules or log again")
	}

	restored, err := svc.Get(ctx, original.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if restored.Week != 1 || restored.TimeSlotID != f.slot1.ID {
		t.Fatalf("timetable changed on repeated revert: %+v", restored)
	}
	_, total, err := svc.ListAdjustments(ctx, 1, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected exactly move + revert logs, got %d", total)
	}
}

// TestRevertRejectedWhenTargetOccupied verifies the revert is refused and the
// timetable/history stay unchanged when the original position is now held by
// another teacher, class or classroom.
func TestRevertRejectedWhenTargetOccupied(t *testing.T) {
	ctx := context.Background()
	db := newScheduleTestDB(t)
	f := seedRevertFixture(t, db)

	lesson := &model.Schedule{Week: 1, DayOfWeek: 1, TimeSlotID: f.slot1.ID, ClassroomID: f.room1.ID, TeacherID: f.teacher1.ID, ClassID: f.class1.ID, CourseID: f.course.ID}
	mustCreate(t, db, lesson)
	svc := newScheduleService(t, db)

	moveResp, err := svc.Move(ctx, &dto.MoveScheduleRequest{
		ScheduleID: lesson.ID, Week: 2, DayOfWeek: 3, TimeSlotID: f.slot2.ID, ClassroomID: f.room2.ID,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}

	// Another lesson now occupies the exact original position.
	occupier := &model.Schedule{Week: 1, DayOfWeek: 1, TimeSlotID: f.slot1.ID, ClassroomID: f.room1.ID, TeacherID: f.teacher2.ID, ClassID: f.class2.ID, CourseID: f.course.ID}
	mustCreate(t, db, occupier)

	if _, err := svc.RevertAdjustment(ctx, moveResp.LogID); err == nil {
		t.Fatal("expected revert to be rejected when classroom is occupied")
	}

	// Timetable stays at the moved position.
	current, err := svc.Get(ctx, lesson.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if current.Week != 2 || current.ClassroomID != f.room2.ID {
		t.Fatalf("timetable must stay pre-revert, got %+v", current)
	}
	// History gains no revert entry.
	_, total, err := svc.ListAdjustments(ctx, 1, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 {
		t.Fatalf("history must stay pre-revert (1 move log), got %d", total)
	}

	// Once the occupier is gone, the revert succeeds.
	if err := db.Delete(occupier).Error; err != nil {
		t.Fatal(err)
	}
	revertResp, err := svc.RevertAdjustment(ctx, moveResp.LogID)
	if err != nil {
		t.Fatalf("revert after freeing position: %v", err)
	}
	if len(revertResp.Schedules) != 1 {
		t.Fatalf("expected 1 restored schedule, got %d", len(revertResp.Schedules))
	}
}

// TestRevertRejectedWhenTeacherForbidden verifies a teacher's current
// unavailable periods block restoring a lesson into that period.
func TestRevertRejectedWhenTeacherForbidden(t *testing.T) {
	ctx := context.Background()
	db := newScheduleTestDB(t)
	f := seedRevertFixture(t, db)

	lesson := &model.Schedule{Week: 1, DayOfWeek: 1, TimeSlotID: f.slot1.ID, ClassroomID: f.room1.ID, TeacherID: f.teacher1.ID, ClassID: f.class1.ID, CourseID: f.course.ID}
	mustCreate(t, db, lesson)
	svc := newScheduleService(t, db)

	moveResp, err := svc.Move(ctx, &dto.MoveScheduleRequest{
		ScheduleID: lesson.ID, Week: 2, DayOfWeek: 3, TimeSlotID: f.slot2.ID, ClassroomID: f.room2.ID,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}

	// The teacher now forbids the original period code "1".
	f.teacher1.UnavailableSlots = []string{"1"}
	if err := db.Save(f.teacher1).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := svc.RevertAdjustment(ctx, moveResp.LogID); err == nil {
		t.Fatal("expected revert to be rejected for forbidden teacher slot")
	}
	current, err := svc.Get(ctx, lesson.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if current.Week != 2 {
		t.Fatalf("timetable must stay pre-revert, got week %d", current.Week)
	}
}

// TestRevertSwapRestoresBothSides verifies a swap captures both lessons and
// undoing it restores both positions, then is idempotent.
func TestRevertSwapRestoresBothSides(t *testing.T) {
	ctx := context.Background()
	db := newScheduleTestDB(t)
	f := seedRevertFixture(t, db)

	a := &model.Schedule{Week: 1, DayOfWeek: 1, TimeSlotID: f.slot1.ID, ClassroomID: f.room1.ID, TeacherID: f.teacher1.ID, ClassID: f.class1.ID, CourseID: f.course.ID}
	b := &model.Schedule{Week: 2, DayOfWeek: 4, TimeSlotID: f.slot2.ID, ClassroomID: f.room2.ID, TeacherID: f.teacher2.ID, ClassID: f.class2.ID, CourseID: f.course.ID}
	mustCreate(t, db, a)
	mustCreate(t, db, b)
	svc := newScheduleService(t, db)

	swapResp, err := svc.Swap(ctx, &dto.SwapScheduleRequest{ScheduleAID: a.ID, ScheduleBID: b.ID})
	if err != nil {
		t.Fatalf("swap: %v", err)
	}

	revertResp, err := svc.RevertAdjustment(ctx, swapResp.LogID)
	if err != nil {
		t.Fatalf("revert swap: %v", err)
	}
	if len(revertResp.Schedules) != 2 {
		t.Fatalf("expected 2 restored schedules, got %d", len(revertResp.Schedules))
	}

	gotA, err := svc.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("get a: %v", err)
	}
	gotB, err := svc.Get(ctx, b.ID)
	if err != nil {
		t.Fatalf("get b: %v", err)
	}
	if gotA.Week != 1 || gotA.TimeSlotID != f.slot1.ID || gotA.ClassroomID != f.room1.ID {
		t.Fatalf("lesson a not restored: %+v", gotA)
	}
	if gotB.Week != 2 || gotB.DayOfWeek != 4 || gotB.TimeSlotID != f.slot2.ID || gotB.ClassroomID != f.room2.ID {
		t.Fatalf("lesson b not restored: %+v", gotB)
	}

	again, err := svc.RevertAdjustment(ctx, swapResp.LogID)
	if err != nil {
		t.Fatalf("second revert: %v", err)
	}
	if !again.AlreadyReverted {
		t.Fatal("swap revert must be idempotent")
	}
}

// TestRevertUnknownLogReturnsNotFound verifies a missing log id is not found.
func TestRevertUnknownLogReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	db := newScheduleTestDB(t)
	_ = seedRevertFixture(t, db)
	svc := newScheduleService(t, db)

	if _, err := svc.RevertAdjustment(ctx, 99999); err == nil {
		t.Fatal("expected not found for unknown log")
	}
}

// TestRevertRejectARevertLog verifies undo logs themselves cannot be undone.
func TestRevertRejectARevertLog(t *testing.T) {
	ctx := context.Background()
	db := newScheduleTestDB(t)
	f := seedRevertFixture(t, db)

	lesson := &model.Schedule{Week: 1, DayOfWeek: 1, TimeSlotID: f.slot1.ID, ClassroomID: f.room1.ID, TeacherID: f.teacher1.ID, ClassID: f.class1.ID, CourseID: f.course.ID}
	mustCreate(t, db, lesson)
	svc := newScheduleService(t, db)

	moveResp, err := svc.Move(ctx, &dto.MoveScheduleRequest{
		ScheduleID: lesson.ID, Week: 2, DayOfWeek: 3, TimeSlotID: f.slot2.ID, ClassroomID: f.room2.ID,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	revertResp, err := svc.RevertAdjustment(ctx, moveResp.LogID)
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if _, err := svc.RevertAdjustment(ctx, revertResp.LogID); err == nil {
		t.Fatal("expected reverting a revert log to be rejected")
	}
}
