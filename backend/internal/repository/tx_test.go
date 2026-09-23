package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gbschedule/gbschedule/internal/model"
	"github.com/gbschedule/gbschedule/internal/repository"
)

// TestTransactionRollback verifies repository writes performed inside a failed
// transaction are all rolled back together.
func TestTransactionRollback(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	logs := repository.NewAdjustmentLogRepository(db)
	schedules := repository.NewScheduleRepository(db)
	tx := repository.NewTransactionManager(db)

	entry := &model.AdjustmentLog{Action: "move", Detail: "{}"}
	if err := logs.Create(ctx, entry); err != nil {
		t.Fatalf("create log: %v", err)
	}

	boom := errors.New("boom")
	err := tx.WithinTransaction(ctx, func(txCtx context.Context) error {
		if err := schedules.CreateBatch(txCtx, []model.Schedule{{
			Week: 1, DayOfWeek: 1, TimeSlotID: 1, ClassroomID: 1, TeacherID: 1, ClassID: 1, CourseID: 1,
		}}); err != nil {
			return err
		}
		undo := &model.AdjustmentLog{Action: "undo", Detail: "{}"}
		if err := logs.Create(txCtx, undo); err != nil {
			return err
		}
		if err := logs.MarkUndone(txCtx, entry.ID, undo.ID); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected sentinel error from transaction, got %v", err)
	}

	// Nothing written inside the failed transaction survives.
	list, _, err := logs.List(ctx, 1, 10)
	if err != nil {
		t.Fatalf("list logs: %v", err)
	}
	if len(list) != 1 || list[0].ID != entry.ID {
		t.Fatalf("expected only the pre-existing log, got %+v", list)
	}
	if list[0].UndoneBy != nil {
		t.Fatalf("expected undone_by to be rolled back, got %d", *list[0].UndoneBy)
	}
	items, err := schedules.List(ctx, repository.ScheduleFilter{})
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected schedule writes to be rolled back, got %+v", items)
	}
}

// TestTransactionCommit verifies writes inside a successful transaction are
// visible afterwards and the ctx tx participates for every repository.
func TestTransactionCommit(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	logs := repository.NewAdjustmentLogRepository(db)
	tx := repository.NewTransactionManager(db)

	entry := &model.AdjustmentLog{Action: "move", Detail: "{}"}
	if err := logs.Create(ctx, entry); err != nil {
		t.Fatalf("create log: %v", err)
	}

	var undoID uint
	if err := tx.WithinTransaction(ctx, func(txCtx context.Context) error {
		undo := &model.AdjustmentLog{Action: "undo", Detail: "{}"}
		if err := logs.Create(txCtx, undo); err != nil {
			return err
		}
		undoID = undo.ID
		return logs.MarkUndone(txCtx, entry.ID, undo.ID)
	}); err != nil {
		t.Fatalf("commit transaction: %v", err)
	}

	got, err := logs.GetByID(ctx, entry.ID)
	if err != nil {
		t.Fatalf("get log: %v", err)
	}
	if got.UndoneBy == nil || *got.UndoneBy != undoID {
		t.Fatalf("expected undone_by=%d, got %v", undoID, got.UndoneBy)
	}
}

// TestMarkUndoneIsSingleShot verifies the second MarkUndone call fails so a
// repeat undo cannot take effect at the repository layer.
func TestMarkUndoneIsSingleShot(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	logs := repository.NewAdjustmentLogRepository(db)

	entry := &model.AdjustmentLog{Action: "move", Detail: "{}"}
	if err := logs.Create(ctx, entry); err != nil {
		t.Fatalf("create log: %v", err)
	}
	if err := logs.MarkUndone(ctx, entry.ID, 100); err != nil {
		t.Fatalf("first mark undone: %v", err)
	}
	err := logs.MarkUndone(ctx, entry.ID, 200)
	if !errors.Is(err, repository.ErrAdjustmentAlreadyUndone) {
		t.Fatalf("expected ErrAdjustmentAlreadyUndone, got %v", err)
	}
	got, err := logs.GetByID(ctx, entry.ID)
	if err != nil {
		t.Fatalf("get log: %v", err)
	}
	if got.UndoneBy == nil || *got.UndoneBy != 100 {
		t.Fatalf("expected original undo id 100 to remain, got %v", got.UndoneBy)
	}
}
