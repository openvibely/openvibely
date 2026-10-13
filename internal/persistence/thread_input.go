package persistence

import (
	"errors"
)

var (
	ErrNoActiveTurn      = errors.New("no active turn")
	ErrExpectedTurnEmpty = errors.New("expected turn id is required")
	ErrActiveTurnChanged = errors.New("active turn changed")
	ErrInputNotPending   = errors.New("input is no longer pending")
	ErrTaskModelChanged  = errors.New("task model changed before queued input claim")
)
