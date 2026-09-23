package service

import "errors"

var (
	// ErrNotFound indicates a requested record does not exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalid indicates malformed business input.
	ErrInvalid = errors.New("invalid input")
	// ErrConflict indicates a constraint violation.
	ErrConflict = errors.New("resource conflict")
	// errAdjustmentAlreadyUndone is used inside an undo transaction to signal
	// a concurrent repeat undo; it is translated to an idempotent response.
	errAdjustmentAlreadyUndone = errors.New("adjustment already undone")
)
