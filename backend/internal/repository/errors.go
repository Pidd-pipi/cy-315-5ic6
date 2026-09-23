package repository

import "errors"

var (
	// ErrNotFound is returned when a requested record does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConstraint is returned when a database constraint rejects a write.
	ErrConstraint = errors.New("constraint violation")
	// ErrAdjustmentAlreadyUndone is returned when an undo tries to revert a
	// change that has already been undone.
	ErrAdjustmentAlreadyUndone = errors.New("adjustment already undone")
)
