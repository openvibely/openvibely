package models

import "fmt"

// ValidateGlobalWorkerLimit validates the persisted global worker limit.
// Zero means unlimited; positive values are finite ceilings.
func ValidateGlobalWorkerLimit(maxWorkers int) error {
	if maxWorkers < 0 {
		return fmt.Errorf("global max_workers must be non-negative")
	}
	return nil
}

// NormalizeProjectWorkerLimit returns the canonical comparison value for a
// project-specific worker limit. Nil, zero, and non-positive legacy values mean
// no project cap.
func NormalizeProjectWorkerLimit(maxWorkers *int) int {
	if maxWorkers == nil || *maxWorkers <= 0 {
		return 0
	}
	return *maxWorkers
}

// ProjectWorkerLimitsEqual reports whether two project-specific worker limits
// have the same effective cap.
func ProjectWorkerLimitsEqual(a, b *int) bool {
	return NormalizeProjectWorkerLimit(a) == NormalizeProjectWorkerLimit(b)
}

// ProjectWorkerLimitIncrease reports whether changing a project-specific worker
// limit increases admission capacity. Clearing a finite cap increases capacity;
// changing an inherited/unlimited cap never does.
func ProjectWorkerLimitIncrease(oldLimit, newLimit *int) bool {
	oldValue := NormalizeProjectWorkerLimit(oldLimit)
	newValue := NormalizeProjectWorkerLimit(newLimit)
	if oldValue == 0 {
		return false
	}
	return newValue == 0 || newValue > oldValue
}

// ValidateProjectWorkerLimit validates a project-specific worker limit against
// the configured global limit. A nil or zero project value means no project cap.
func ValidateProjectWorkerLimit(maxWorkers *int, globalMaxWorkers int) error {
	if err := ValidateGlobalWorkerLimit(globalMaxWorkers); err != nil {
		return err
	}
	if maxWorkers == nil || *maxWorkers == 0 {
		return nil
	}
	if *maxWorkers < 0 {
		return fmt.Errorf("project max_workers must be non-negative")
	}
	if globalMaxWorkers > 0 && *maxWorkers > globalMaxWorkers {
		return fmt.Errorf("project max_workers %d exceeds global worker limit %d", *maxWorkers, globalMaxWorkers)
	}
	return nil
}
