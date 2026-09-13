package update

import (
	"strings"
	"testing"
	"time"
)

func TestApplyIntegrationTimeoutOverrides(t *testing.T) {
	const (
		waitEnv       = "OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS"
		validationEnv = "OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS"
	)

	tests := []struct {
		name            string
		waitValue       string
		validationValue string
		wantWait        time.Duration
		wantValidation  time.Duration
		wantError       string
	}{
		{
			name:            "valid millisecond overrides",
			waitValue:       "2500",
			validationValue: "7500",
			wantWait:        2500 * time.Millisecond,
			wantValidation:  7500 * time.Millisecond,
		},
		{
			name:            "wait override unset",
			validationValue: "7500",
			wantWait:        11 * time.Second,
			wantValidation:  7500 * time.Millisecond,
		},
		{
			name:           "validation override unset",
			waitValue:      "2500",
			wantWait:       2500 * time.Millisecond,
			wantValidation: 22 * time.Second,
		},
		{
			name:            "invalid wait timeout",
			waitValue:       "not-a-duration",
			validationValue: "7500",
			wantWait:        11 * time.Second,
			wantValidation:  22 * time.Second,
			wantError:       "parse update integration wait timeout",
		},
		{
			name:            "invalid validation timeout",
			waitValue:       "2500",
			validationValue: "not-a-duration",
			wantWait:        2500 * time.Millisecond,
			wantValidation:  22 * time.Second,
			wantError:       "parse update integration validation timeout",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(waitEnv, test.waitValue)
			t.Setenv(validationEnv, test.validationValue)
			waitTimeout := 11 * time.Second
			validationTimeout := 22 * time.Second

			err := ApplyIntegrationTimeoutOverrides(&waitTimeout, &validationTimeout)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("ApplyIntegrationTimeoutOverrides() error = %v", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("ApplyIntegrationTimeoutOverrides() error = %v, want %q", err, test.wantError)
				}
			}
			if waitTimeout != test.wantWait {
				t.Fatalf("wait timeout = %s, want %s", waitTimeout, test.wantWait)
			}
			if validationTimeout != test.wantValidation {
				t.Fatalf("validation timeout = %s, want %s", validationTimeout, test.wantValidation)
			}
		})
	}
}
