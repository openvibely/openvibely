package models

import "encoding/json"

// ConversationOwner identifies an existing execution or thread checkpoint.
// It never creates a second context identity alongside those records.
type ConversationOwner struct {
	ExecutionID string
	ScopeType   string
	ScopeID     string
}

type ConversationLane string

const (
	ConversationProvider ConversationLane = "provider"
	ConversationDisplay  ConversationLane = "display"
	ConversationMetadata ConversationLane = "metadata"
)

type ConversationOperation string

const (
	ConversationPut    ConversationOperation = "put"
	ConversationAppend ConversationOperation = "append"
	ConversationRemove ConversationOperation = "remove"
	ConversationReset  ConversationOperation = "reset"
)

// ConversationItem is a reconstructed item at Revision. Position/ID form the
// stable keyset cursor for cold-page reads; Revision is a change-stream cursor.
type ConversationItem struct {
	ID       string          `json:"id"`
	Position int64           `json:"position"`
	Revision int64           `json:"revision"`
	Payload  json.RawMessage `json:"payload"`
}

// ConversationPage is read from one database snapshot. Changes after Cursor
// cannot be lost between loading a page and subscribing to the stream.
type ConversationPage struct {
	Items  []ConversationItem `json:"items"`
	Cursor int64              `json:"cursor"`
}

// ConversationChange is an immutable journal record. Sequence orders changes;
// ItemID/Position identify a stable logical item. An edit to an old item gets a
// new Sequence, so reconnecting clients cannot miss it.
// Provider payloads must never be included in public display subscriptions.
type ConversationChange struct {
	Sequence         int64                 `json:"sequence"`
	Generation       int64                 `json:"generation"`
	Lane             ConversationLane      `json:"lane"`
	ItemID           string                `json:"item_id"`
	Position         int64                 `json:"position"`
	Operation        ConversationOperation `json:"operation"`
	Payload          json.RawMessage       `json:"payload"`
	PreviousRevision int64                 `json:"previous_revision"`
	MutationID       string                `json:"-"`
}
