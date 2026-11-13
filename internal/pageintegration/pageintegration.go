// Package pageintegration defines the surface-neutral contract between a page
// in the main content area and the page-aware side chat. Plugin pages adapt to
// it today; built-in pages (Analytics, Automations) can adopt it later without
// any dependency on plugins or iframes. See docs/plugin-pages.md.
package pageintegration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Limits shared by every integration.
const (
	MaxContextBytes     = 64 << 10
	MaxOperationInput   = 64 << 10
	MaxOperationOutput  = 256 << 10
	MaxContextSelection = 200
)

// PageRef identifies one concrete page instance. ProjectID is always resolved
// server-side from the owning record; it is never taken from the browser.
type PageRef struct {
	Kind       string `json:"kind"`
	PageID     string `json:"page_id"`
	InstanceID string `json:"instance_id"`
	ProjectID  string `json:"project_id"`
	Title      string `json:"title"`
}

func (r PageRef) Key() string { return r.Kind + ":" + r.InstanceID + ":" + r.PageID }

// DateRange is an optional explicit time window shared by the page.
type DateRange struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// Context is what a page explicitly shares. Nothing is scraped from the DOM.
type Context struct {
	Summary   string            `json:"summary,omitempty"`
	Selection []string          `json:"selection,omitempty"`
	Filters   map[string]string `json:"filters,omitempty"`
	DateRange *DateRange        `json:"date_range,omitempty"`
	Data      json.RawMessage   `json:"data,omitempty"`
}

// Snapshot is a Context captured for one chat turn plus its provenance.
type Snapshot struct {
	Page       PageRef   `json:"page"`
	Context    Context   `json:"context"`
	Source     string    `json:"source"`
	CapturedAt time.Time `json:"captured_at"`
	Revision   int64     `json:"revision"`
}

// ValidateContext enforces size and shape limits before a context is stored.
func ValidateContext(c Context) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if len(raw) > MaxContextBytes {
		return fmt.Errorf("page context exceeds %d bytes", MaxContextBytes)
	}
	if len(c.Selection) > MaxContextSelection {
		return fmt.Errorf("page context selection exceeds %d items", MaxContextSelection)
	}
	if len(c.Data) > 0 && !json.Valid(c.Data) {
		return errors.New("page context data must be valid JSON")
	}
	return nil
}

// OperationKind separates side-effect-free reads from consequential actions.
type OperationKind string

const (
	OperationRead   OperationKind = "read"
	OperationAction OperationKind = "action"
)

// Operation is defined once and invoked identically by page buttons and agent tools.
type Operation struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Kind         OperationKind   `json:"kind"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	Permissions  []string        `json:"permissions,omitempty"`
	// Deprecated: accepted for old packages; all declared operations are tools.
	ExposeAsTool bool     `json:"expose_as_tool"`
	Emits        []string `json:"emits,omitempty"`
}

var operationNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)

// ValidateOperation checks names, kinds, and that schemas compile.
func ValidateOperation(op Operation) error {
	if !operationNamePattern.MatchString(op.Name) {
		return fmt.Errorf("operation name %q must match %s", op.Name, operationNamePattern)
	}
	if strings.TrimSpace(op.Description) == "" {
		return fmt.Errorf("operation %q requires a description", op.Name)
	}
	if op.Kind != OperationRead && op.Kind != OperationAction {
		return fmt.Errorf("operation %q kind must be read or action", op.Name)
	}
	for label, schema := range map[string]json.RawMessage{"input_schema": op.InputSchema, "output_schema": op.OutputSchema} {
		if len(schema) == 0 {
			continue
		}
		if _, err := compileSchema(schema); err != nil {
			return fmt.Errorf("operation %q %s: %w", op.Name, label, err)
		}
	}
	return nil
}

func compileSchema(schema json.RawMessage) (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft2020
	c.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errors.New("remote schema references are not allowed")
	}
	if err := c.AddResource("op.json", strings.NewReader(string(schema))); err != nil {
		return nil, err
	}
	return c.Compile("op.json")
}

// ValidateJSON validates a payload against an optional schema.
func ValidateJSON(schema, payload json.RawMessage) error {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if !json.Valid(payload) {
		return errors.New("payload must be valid JSON")
	}
	if len(schema) == 0 {
		return nil
	}
	compiled, err := compileSchema(schema)
	if err != nil {
		return err
	}
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		return err
	}
	return compiled.Validate(v)
}

// Caller describes who invoked an operation.
type Caller struct {
	PrincipalID string
	Via         string // "page" or "agent"
	ExecutionID string
}

// Event is published to subscribers of one page instance.
type Event struct {
	Type      string          `json:"type"`
	Operation string          `json:"operation,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	At        time.Time       `json:"at"`
}

// Integration is the server-side contract every page-aware surface implements.
type Integration interface {
	Ref() PageRef
	Operations() []Operation
	// Invoke runs an already-authorized operation. Implementations must not
	// trust project identifiers inside input.
	Invoke(ctx context.Context, caller Caller, op string, input json.RawMessage) (json.RawMessage, error)
}

// FindOperation returns the named operation.
func FindOperation(i Integration, name string) (Operation, bool) {
	for _, op := range i.Operations() {
		if op.Name == name {
			return op, true
		}
	}
	return Operation{}, false
}

// ErrOperationNotFound is returned for unknown operations.
var ErrOperationNotFound = errors.New("operation not found")

// Run validates input and output around Invoke. Page buttons and agent tools both go through it.
func Run(ctx context.Context, i Integration, caller Caller, name string, input json.RawMessage) (json.RawMessage, error) {
	op, ok := FindOperation(i, name)
	if !ok {
		return nil, ErrOperationNotFound
	}
	if len(input) > MaxOperationInput {
		return nil, fmt.Errorf("operation input exceeds %d bytes", MaxOperationInput)
	}
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	if err := ValidateJSON(op.InputSchema, input); err != nil {
		return nil, fmt.Errorf("invalid input for %s: %w", name, err)
	}
	out, err := i.Invoke(ctx, caller, name, input)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		out = json.RawMessage(`{}`)
	}
	if len(out) > MaxOperationOutput {
		return nil, fmt.Errorf("operation output exceeds %d bytes", MaxOperationOutput)
	}
	if err := ValidateJSON(op.OutputSchema, out); err != nil {
		return nil, fmt.Errorf("invalid output from %s: %w", name, err)
	}
	return out, nil
}
