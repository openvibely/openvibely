package persistence

import "errors"

// ErrNotFound reports that a store lookup or keyed mutation matched no record.
var ErrNotFound = errors.New("record not found")
