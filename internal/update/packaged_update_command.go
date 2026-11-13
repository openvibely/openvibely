package update

import (
	"context"
	"io"
)

// PackagedUpdateHelperOptions selects the helpers supported by an entrypoint.
// Executable helpers are always supported; app-bundle helpers are opt-in.
type PackagedUpdateHelperOptions struct {
	AppBundle bool
}

// RunPackagedUpdateHelperCommand restores and runs a recognized packaged helper.
// Unsupported commands return false without reading stdin, allowing ordinary
// startup to continue. Errors from recognized commands belong to the caller.
func RunPackagedUpdateHelperCommand(ctx context.Context, command string, args []string, stdin io.Reader, options PackagedUpdateHelperOptions) (bool, error) {
	switch command {
	case ExecutableUpdateHelperCommand:
		cfg, err := ParseExecutableUpdateHelperArgs(args)
		if err != nil {
			return true, err
		}
		if cfg.RelaunchMetadataPath != "" {
			err = LoadExecutableUpdateHelperRelaunchFile(cfg.RelaunchMetadataPath, &cfg)
		} else {
			err = LoadExecutableUpdateHelperRelaunch(stdin, &cfg)
		}
		if err != nil {
			return true, err
		}
		if err := ApplyIntegrationTimeoutOverrides(&cfg.WaitTimeout, &cfg.ValidationTimeout); err != nil {
			return true, err
		}
		return true, RunExecutableUpdateHelper(ctx, cfg)
	case AppBundleUpdateHelperCommand:
		if !options.AppBundle {
			return false, nil
		}
		cfg, err := ParseAppBundleUpdateHelperArgs(args)
		if err != nil {
			return true, err
		}
		if err := LoadAppBundleUpdateHelperRelaunch(stdin, &cfg); err != nil {
			return true, err
		}
		if err := ApplyIntegrationTimeoutOverrides(&cfg.WaitTimeout, &cfg.ValidationTimeout); err != nil {
			return true, err
		}
		return true, RunAppBundleUpdateHelper(ctx, cfg)
	default:
		return false, nil
	}
}
