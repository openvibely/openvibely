package service

import "context"

type additionalProjectInstructionsKey struct{}
type selectedMemoryHandlesKey struct{}

func withAdditionalProjectInstructions(ctx context.Context, value string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if value == "" {
		return ctx
	}
	return context.WithValue(ctx, additionalProjectInstructionsKey{}, value)
}

func additionalProjectInstructionsFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(additionalProjectInstructionsKey{}).(string)
	return v
}

func combineAdditionalProjectInstructions(ctx context.Context, base string) string {
	extra := additionalProjectInstructionsFromContext(ctx)
	if extra == "" {
		return base
	}
	if base == "" {
		return extra
	}
	return extra + "\n\n" + base
}

// WithSelectedMemoryHandles annotates ctx with the permitted managed memory handles
// for the current model turn.
func WithSelectedMemoryHandles(ctx context.Context, handles []string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, selectedMemoryHandlesKey{}, append([]string(nil), handles...))
}

// SelectedMemoryHandlesFromContext returns the permitted managed memory handles
// for the current model turn. The historical name remains for custom integrations.
func SelectedMemoryHandlesFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	handles, _ := ctx.Value(selectedMemoryHandlesKey{}).([]string)
	return append([]string(nil), handles...)
}
