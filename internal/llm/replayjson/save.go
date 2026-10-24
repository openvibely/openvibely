package replayjson

import "context"

// Saver owns a single encode/write/notify operation. It retains no history or
// encoded data between calls. Providers supply their persistence metadata and
// choose the context (including whether cancellation should be detached).
type Saver struct {
	// Persist must store the supplied JSON without encoding it again.
	Persist func(context.Context, string) error
	// AfterSave receives the same JSON, only after persistence succeeds.
	AfterSave func(string)
	// EncodeError optionally preserves a provider's encoding-error context.
	EncodeError func(error) error
}

func (s Saver) Save(ctx context.Context, history any) error {
	encoded, err := Encode(history)
	if err != nil {
		if s.EncodeError != nil {
			return s.EncodeError(err)
		}
		return err
	}
	if err := s.Persist(ctx, encoded); err != nil {
		return err
	}
	if s.AfterSave != nil {
		s.AfterSave(encoded)
	}
	return nil
}
