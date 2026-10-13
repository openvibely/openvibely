package persistence

import (
	"errors"
)

var (
	ErrOAuthConnectionChanged     = errors.New("OAuth account changed during sign-in; connect OAuth again")
	ErrOAuthConnectionNotFound    = errors.New("OAuth connection not found")
	ErrOAuthConnectionInvalidMove = errors.New("invalid OAuth connection model move")
	ErrOAuthConnectionLinked      = errors.New("OAuth connection has linked models")
)
