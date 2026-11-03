package transcript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

const ReplayGuardID = "replay-guard"

// Recorder belongs to a single provider invocation, not a global execution-ID
// cache. Frozen values share immutable strings but copy mutable maps/slices, so
// later parallel-tool updates cannot change the last persisted checkpoint.
// Heads retain payload digests rather than another full serialized history;
// frozen values supply recovery and digests detect equivalent encodings.
type Recorder struct {
	mu               sync.Mutex
	store            persistence.ConversationStore
	owner            models.ConversationOwner
	loaded           bool
	heads            map[string]models.ConversationChange
	hashes           map[string][32]byte
	frozen           map[string]any
	itemIDs          []string
	recovery         []any
	preserveExisting bool
	initialized      bool
	frame            int
	prefix           []models.ConversationChange
}

func NewRecorder(store persistence.ConversationStore, owner models.ConversationOwner) *Recorder {
	return &Recorder{store: store, owner: owner}
}

// replacePrevious switches an invocation from append to explicit replacement.
func (r *Recorder) replacePrevious() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.preserveExisting = false
	r.initialized = true
	r.prefix = nil
	r.frame = 0
	r.itemIDs = nil
	r.recovery = nil
	r.frozen = nil
	r.loaded = false
}

func (r *Recorder) load(ctx context.Context) error {
	heads, err := r.store.ReadConversationHeads(ctx, r.owner, "", 0)
	if err != nil {
		return err
	}
	if !r.initialized {
		if r.preserveExisting {
			for _, h := range heads {
				if !strings.HasPrefix(h.ItemID, ReplayRecordPrefix) || h.Operation == models.ConversationRemove {
					continue
				}
				r.prefix = append(r.prefix, h)
				parts := strings.Split(h.ItemID, ":")
				if len(parts) >= 3 {
					frame, err := strconv.Atoi(parts[1])
					if err != nil {
						return err
					}
					r.frame = max(r.frame, frame+1)
				}
			}
		}
		r.initialized = true
	}
	r.heads = make(map[string]models.ConversationChange, len(heads))
	r.hashes = make(map[string][32]byte, len(heads))
	r.frozen = make(map[string]any)
	for _, h := range heads {
		if strings.HasPrefix(h.ItemID, ReplayRecordPrefix) || h.ItemID == ReplayGuardID {
			r.hashes[h.ItemID] = sha256.Sum256(h.Payload)
			h.Payload = nil
			r.heads[h.ItemID] = h
		}
	}
	r.loaded = true
	return nil
}

// Save serializes only new or changed items. The header and all item changes
// commit together; recovery advances only after the store confirms the batch.
func (r *Recorder) Save(ctx context.Context, header ReplayHeader, items []any) error {
	return r.save(ctx, header, items, 0)
}

// SaveChanged persists an explicitly changed suffix. Callers must report zero
// after context rewrites/compaction, and the first affected index after edits.
// An external writer or failed save invalidates the prefix shortcut.
func (r *Recorder) SaveChanged(ctx context.Context, header ReplayHeader, items []any, firstChanged int) error {
	if firstChanged < 0 || firstChanged > len(items) {
		return fmt.Errorf("invalid changed-history boundary")
	}
	return r.save(ctx, header, items, firstChanged)
}

func (r *Recorder) save(ctx context.Context, header ReplayHeader, items []any, firstChanged int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	header.ReasoningAuthoritative = true
	if !r.loaded {
		firstChanged = 0
		if err := r.load(ctx); err != nil {
			return err
		}
	} else {
		revision, err := r.store.ConversationRevision(ctx, r.owner, models.ConversationMetadata, ReplayGuardID, 0)
		if err != nil {
			return err
		}
		// Steering/approval may commit through another transactional writer.
		if revision != r.heads[ReplayGuardID].Sequence {
			firstChanged = 0
			if err := r.load(ctx); err != nil {
				return err
			}
		}
	}
	if firstChanged > len(r.recovery) {
		firstChanged = 0
	}
	nextFrozen := make(map[string]any, len(items)-firstChanged)
	var retained map[string]bool
	if firstChanged == 0 || len(items) < len(r.recovery) {
		retained = make(map[string]bool, len(items)+2)
	}
	var changes []models.ConversationChange
	stage := func(id string, lane models.ConversationLane, position int64, value any) error {
		if retained != nil {
			retained[id] = true
		}
		old, ok := r.heads[id]
		if frozen, exists := r.frozen[id]; exists && ok && old.Operation == models.ConversationPut && equalReplayValue(frozen, value) {
			nextFrozen[id] = frozen
			return nil
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return &persistence.ReplayEncodingError{Err: err}
		}
		nextFrozen[id] = freezeReplayValue(value)
		if ok && old.Operation == models.ConversationPut && r.hashes[id] == sha256.Sum256(raw) {
			return nil
		}
		changes = append(changes, models.ConversationChange{ItemID: id, Lane: lane, Position: position, Operation: models.ConversationPut, Payload: raw, PreviousRevision: old.Sequence, MutationID: persistence.NewID()})
		return nil
	}
	for len(r.itemIDs) < len(items) {
		r.itemIDs = append(r.itemIDs, ReplayItemID(r.frame, len(r.itemIDs)))
	}
	for i := firstChanged; i < len(items); i++ {
		item := items[i]
		if err := stage(r.itemIDs[i], models.ConversationProvider, int64(r.frame)<<32|int64(i), item); err != nil {
			return err
		}
	}
	if err := stage(ReplayHeaderID(r.frame), models.ConversationMetadata, int64(r.frame), ReplayFrame{ReplayHeader: header, Count: len(items)}); err != nil {
		return err
	}
	// Prefix entries are immutable for this invocation. A steering transaction
	// may replace the physical frame layout; restore the captured entries atomically
	// with our new frame rather than accidentally duplicating that transaction's
	// full conversation snapshot.
	for _, prior := range r.prefix {
		if retained != nil {
			retained[prior.ItemID] = true
		}
		old, ok := r.heads[prior.ItemID]
		if ok && old.Operation == models.ConversationPut && r.hashes[prior.ItemID] == sha256.Sum256(prior.Payload) {
			continue
		}
		prior.PreviousRevision = old.Sequence
		prior.MutationID = persistence.NewID()
		changes = append(changes, prior)
	}
	if retained != nil {
		retained[ReplayGuardID] = true
		for i := 0; i < firstChanged; i++ {
			retained[r.itemIDs[i]] = true
		}
		for id, old := range r.heads {
			if retained[id] || old.Operation == models.ConversationRemove {
				continue
			}
			changes = append(changes, models.ConversationChange{ItemID: id, Lane: old.Lane, Position: old.Position, Operation: models.ConversationRemove, Payload: json.RawMessage(`{}`), PreviousRevision: old.Sequence, MutationID: persistence.NewID()})
		}
	}
	if len(changes) > 0 {
		changes = append(changes, models.ConversationChange{ItemID: ReplayGuardID, Lane: models.ConversationMetadata, Position: 0, Operation: models.ConversationPut, Payload: json.RawMessage(`{}`), PreviousRevision: r.heads[ReplayGuardID].Sequence, MutationID: persistence.NewID()})
		var saved []models.ConversationChange
		var err error
		if encoded, ok := r.store.(interface {
			AppendEncodedReplayChanges(context.Context, models.ConversationOwner, []models.ConversationChange) ([]models.ConversationChange, error)
		}); ok {
			saved, err = encoded.AppendEncodedReplayChanges(ctx, r.owner, changes)
		} else {
			saved, err = r.store.AppendConversationChanges(ctx, r.owner, changes)
		}
		if err != nil {
			r.loaded = false
			return fmt.Errorf("save conversation items: %w", err)
		}
		for _, c := range saved {
			r.hashes[c.ItemID] = sha256.Sum256(c.Payload)
			c.Payload = nil
			r.heads[c.ItemID] = c
		}
	}
	for id, value := range nextFrozen {
		r.frozen[id] = value
	}
	if retained != nil {
		for id := range r.frozen {
			if !retained[id] {
				delete(r.frozen, id)
			}
		}
	}
	if items == nil {
		r.recovery = nil
	} else {
		if r.recovery == nil {
			r.recovery = make([]any, 0, len(items))
		}
		if len(items) < len(r.recovery) {
			clear(r.recovery[len(items):])
			r.recovery = r.recovery[:len(items)]
		} else if len(items) > len(r.recovery) {
			r.recovery = append(r.recovery, make([]any, len(items)-len(r.recovery))...)
		}
		for i := firstChanged; i < len(items); i++ {
			r.recovery[i] = r.frozen[r.itemIDs[i]]
		}
	}
	return nil
}

// Recovery captures the committed structured snapshot without encoding JSON.
// It is read-only: callers must not mutate the returned values. Later saves use
// new frozen values, so capturing this slice does not capture live client state.
func (r *Recorder) Recovery() []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recovery == nil {
		return nil
	}
	return append([]any{}, r.recovery...)
}

// equalReplayValue validates the entire prefix: context refreshes and parallel
// tool results can rewrite an earlier item even when the tail is unchanged.
// JSON maps/slices need no reflection or per-entry interface allocations. Typed
// provider DTOs retain the conservative fallback (including custom marshalers).
func equalReplayValue(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case int:
		y, ok := b.(int)
		return ok && x == y
	case json.Number:
		y, ok := b.(json.Number)
		return ok && x == y
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) || (x == nil) != (y == nil) {
			return false
		}
		for key, value := range x {
			other, exists := y[key]
			if !exists || !equalReplayValue(value, other) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) || (x == nil) != (y == nil) {
			return false
		}
		for i, value := range x {
			if !equalReplayValue(value, y[i]) {
				return false
			}
		}
		return true
	case jsonview.Value:
		y, ok := b.(jsonview.Value)
		return ok && bytes.Equal(x.Raw(), y.Raw())
	case json.RawMessage:
		y, ok := b.(json.RawMessage)
		return ok && (x == nil) == (y == nil) && bytes.Equal(x, y)
	default:
		return reflect.DeepEqual(a, b)
	}
}

// Fast JSON-value copies retain immutable strings without reflective map
// iteration. The fallback preserves concrete provider DTO/custom marshaler types.
func freezeReplayValue(value any) any {
	switch v := value.(type) {
	case nil, string, bool, float64, int, json.Number, jsonview.Value:
		return value
	case map[string]any:
		if v == nil {
			return v
		}
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = freezeReplayValue(item)
		}
		return out
	case []any:
		if v == nil {
			return v
		}
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = freezeReplayValue(item)
		}
		return out
	case json.RawMessage:
		return json.RawMessage(bytes.Clone(v))
	default:
		return freezeValue(reflect.ValueOf(value)).Interface()
	}
}

// freezeValue runs only after successful JSON encoding (which rejects cycles).
// Structs retain their concrete type and custom JSON marshaling behavior.
func freezeValue(v reflect.Value) reflect.Value {
	if !v.IsValid() {
		return v
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(freezeValue(v.Elem()))
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(freezeValue(v.Elem()))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(freezeValue(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), freezeValue(iter.Value()))
		}
		return out
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(freezeValue(v.Index(i)))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				out.Field(i).Set(freezeValue(v.Field(i)))
			}
		}
		return out
	default:
		return v
	}
}

func Values[T any](items []T) []any {
	result := make([]any, len(items))
	for i, item := range items {
		result[i] = item
	}
	return result
}

// FreezeItems captures a committed externally-written checkpoint (for example,
// atomic steering acceptance) without constructing a full recovery JSON string.
func FreezeItems(items []any) []any {
	if items == nil {
		return nil
	}
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = freezeReplayValue(item)
	}
	return out
}
