package repository

import "errors"

var (
	// ErrNotFound is returned when a requested record does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConstraint is returned when a database constraint rejects a write.
	ErrConstraint = errors.New("constraint violation")
	// ErrConflict is returned when an optimistic/conditional update loses a
	// race, e.g. stamping a change as reverted that another tx already undid.
	ErrConflict = errors.New("conflict")
)
