package pageplugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

// BridgeVersion is the postMessage bridge protocol version (field "v").
const BridgeVersion = 1

// MaxBridgeMessageBytes bounds one bridge message (host and server both enforce it).
const MaxBridgeMessageBytes = 64 << 10

// Bridge message types a plugin page may send.
const (
	BridgeReady           = "ready"
	BridgeOpInvoke        = "op.invoke"
	BridgeContextSet      = "context.set"
	BridgeAnalyzeRequest  = "analyze.request"
	BridgeEventsSubscribe = "events.subscribe"
)

var bridgeTypes = map[string]bool{BridgeReady: true, BridgeOpInvoke: true, BridgeContextSet: true, BridgeAnalyzeRequest: true, BridgeEventsSubscribe: true}

var bridgeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// BridgeMessage is the v1 envelope sent by a plugin iframe.
type BridgeMessage struct {
	V       int             `json:"v"`
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Handle  string          `json:"handle"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// OpInvokePayload is the payload of op.invoke.
type OpInvokePayload struct {
	Operation string          `json:"operation"`
	Input     json.RawMessage `json:"input,omitempty"`
}

// AnalyzePayload is the payload of analyze.request.
type AnalyzePayload struct {
	Prompt string `json:"prompt"`
}

// ParseBridgeMessage strictly validates a raw bridge envelope against the expected handle.
func ParseBridgeMessage(raw []byte, expectedHandle string) (*BridgeMessage, error) {
	if len(raw) > MaxBridgeMessageBytes {
		return nil, fmt.Errorf("bridge message exceeds %d bytes", MaxBridgeMessageBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m BridgeMessage
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("invalid bridge message: %w", err)
	}
	if m.V != BridgeVersion {
		return nil, fmt.Errorf("unsupported bridge version %d", m.V)
	}
	if !bridgeTypes[m.Type] {
		return nil, fmt.Errorf("unsupported bridge message type %q", m.Type)
	}
	if !bridgeIDPattern.MatchString(m.ID) {
		return nil, errors.New("bridge message id is required")
	}
	if m.Handle == "" || m.Handle != expectedHandle {
		return nil, errors.New("bridge handle mismatch")
	}
	if len(m.Payload) == 0 || string(m.Payload) == "null" {
		m.Payload = json.RawMessage(`{}`)
	}
	if m.Payload[0] != '{' {
		return nil, errors.New("bridge payload must be an object")
	}
	return &m, nil
}

// DecodeStrict decodes a bridge payload, rejecting unknown fields.
func DecodeStrict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
