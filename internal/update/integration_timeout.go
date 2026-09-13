package update

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// ApplyIntegrationTimeoutOverrides applies the optional packaged-update
// integration timeout overrides to the supplied helper timeout fields.
func ApplyIntegrationTimeoutOverrides(waitTimeout, validationTimeout *time.Duration) error {
	if value := os.Getenv("OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS"); value != "" {
		milliseconds, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("parse update integration wait timeout: %w", err)
		}
		*waitTimeout = time.Duration(milliseconds) * time.Millisecond
	}
	if value := os.Getenv("OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS"); value != "" {
		milliseconds, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("parse update integration validation timeout: %w", err)
		}
		*validationTimeout = time.Duration(milliseconds) * time.Millisecond
	}
	return nil
}
