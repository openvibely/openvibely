package persistence

import (
	"errors"
)

var ErrAutomationDispatchInFlight = errors.New("automation has in-flight dispatch work")
