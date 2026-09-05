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
