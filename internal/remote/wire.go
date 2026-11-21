// Package remote routes store calls for entities owned by saved servers over each
// server's live WebSocket, and executes those calls on the owning server.
package remote

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/gob"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"

	"github.com/openvibely/openvibely/internal/cardcursor"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service/authz"
)

func init() {
	gob.Register(map[string]any{})
	gob.Register([]any{})
	gob.Register(json.Number(""))
}

// Self stands in for the caller's user ID; the owning server substitutes its own
// signed-in user, so local user IDs never select remote rows.
const Self = "\x00self"

// Read is the request body of an "op":"read" socket command.
type Read struct {
	Page   *cardcursor.State `json:"page,omitempty"`
	Store  string            `json:"store"`
	Method string            `json:"method"`
	Args   []byte            `json:"args"`
	// Scope narrows an All projects read to these projects (a shared project's copies).
	Scope []string `json:"scope,omitempty"`
}

// Result answers one read. Code identifies sentinel errors.
type Result struct {
	CursorVersion int    `json:"cursor_version,omitempty"`
	Values        []byte `json:"values,omitempty"`
	Error         string `json:"error,omitempty"`
	Code          string `json:"code,omitempty"`
}

var ErrUnavailable = errors.New("remote server unavailable")
var ErrUnsupported = errors.New("operation not supported for remote servers")

func encodeValues(values []reflect.Value) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	for _, v := range values {
		isNil := isNilValue(v)
		if err := enc.Encode(isNil); err != nil {
			return nil, err
		}
		if isNil {
			continue
		}
		if err := enc.EncodeValue(v); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func decodeValues(data []byte, types []reflect.Type) ([]reflect.Value, error) {
	dec := gob.NewDecoder(bytes.NewReader(data))
	out := make([]reflect.Value, len(types))
	for i, t := range types {
		var isNil bool
		if err := dec.Decode(&isNil); err != nil {
			return nil, err
		}
		v := reflect.New(t).Elem()
		if !isNil {
			if err := dec.DecodeValue(v.Addr()); err != nil {
				return nil, err
			}
		}
		out[i] = v
	}
	return out, nil
}

func isNilValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
		return v.IsNil()
	}
	return !v.IsValid()
}

func errorCode(err error) (string, string) {
	var role *authz.InsufficientRole
	switch {
	case err == nil:
		return "", ""
	case errors.As(err, &role):
		return "role:" + role.Role + ":" + string(role.Permission), err.Error()
	case errors.Is(err, persistence.ErrProjectDenied):
		return "denied", err.Error()
	case errors.Is(err, persistence.ErrNotFound):
		return "not_found", err.Error()
	case errors.Is(err, sql.ErrNoRows):
		return "no_rows", err.Error()
	}
	return "", err.Error()
}

func resultError(r Result) error {
	switch {
	case r.Code == "denied":
		return persistence.ErrProjectDenied
	case r.Code == "not_found":
		return persistence.ErrNotFound
	case r.Code == "no_rows":
		return sql.ErrNoRows
	case strings.HasPrefix(r.Code, "role:"):
		parts := strings.SplitN(r.Code, ":", 3)
		if len(parts) == 3 {
			return &authz.InsufficientRole{Role: parts[1], Permission: authz.Permission(parts[2])}
		}
	}
	if r.Error != "" {
		return errors.New(r.Error)
	}
	return nil
}

func secretField(name string) bool {
	return name == "APIKey" || name == "PasswordHash" || strings.HasSuffix(name, "Token") || strings.HasSuffix(name, "Secret") || strings.HasSuffix(name, "Password")
}

// scrubber clears secrets when non-nil. It reports whether the caller manages
// projectID, which keeps fields tagged remote:"manage".
type scrubber func(projectID string) bool

func hasTag(f reflect.StructField, tag string) bool {
	for _, t := range strings.Split(f.Tag.Get("remote"), ",") {
		if t == tag {
			return true
		}
	}
	return false
}

// walk visits every settable string reachable from v, including map keys. name
// is the nearest enclosing struct field name. Fields tagged remote:"raw" (file
// bytes, tokens) are never passed to fn.
func walk(v reflect.Value, fn func(name, s string) string, scrub scrubber, name string, depth int) {
	if depth > 12 || !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			if s := fn(name, v.String()); s != v.String() {
				v.SetString(s)
			}
		}
	case reflect.Pointer:
		if !v.IsNil() {
			walk(v.Elem(), fn, scrub, name, depth+1)
		}
	case reflect.Interface:
		if v.IsNil() || !v.CanSet() {
			return
		}
		inner := v.Elem()
		cp := reflect.New(inner.Type()).Elem()
		cp.Set(inner)
		walk(cp, fn, scrub, name, depth+1)
		v.Set(cp)
	case reflect.Struct:
		if scrub != nil && v.CanAddr() {
			if config, ok := v.Addr().Interface().(*models.LLMConfig); ok {
				connected := config.HasValidOAuthToken()
				config.RemoteOAuthConnected = &connected
			}
		}
		t := v.Type()
		project := ""
		if p := v.FieldByName("ProjectID"); p.IsValid() && p.Kind() == reflect.String {
			project = p.String()
		}
		for i := 0; i < v.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			if scrub != nil {
				var clear bool
				if hasTag(f, "manage") {
					clear = project == "" || !scrub(project)
				} else {
					clear = hasTag(f, "-") || secretField(f.Name)
				}
				if clear {
					if v.Field(i).CanSet() {
						v.Field(i).SetZero()
					}
					continue
				}
			}
			if hasTag(f, "raw") {
				continue
			}
			walk(v.Field(i), fn, scrub, f.Name, depth+1)
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			if v.CanSet() && !v.IsNil() {
				if s := fn("", string(v.Bytes())); s != string(v.Bytes()) {
					v.SetBytes([]byte(s))
				}
			}
			return
		}
		for i := 0; i < v.Len(); i++ {
			walk(v.Index(i), fn, scrub, name, depth+1)
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walk(v.Index(i), fn, scrub, name, depth+1)
		}
	case reflect.Map:
		if v.IsNil() || !v.CanSet() {
			return
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			k := reflect.New(v.Type().Key()).Elem()
			k.Set(iter.Key())
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(iter.Value())
			walk(k, fn, scrub, name, depth+1)
			walk(e, fn, scrub, name, depth+1)
			out.SetMapIndex(k, e)
		}
		v.Set(out)
	}
}

// entityFields name OpenVibely entity references whose values may be non-hex
// (the "default" project, system agents). Other IDs are tagged only when hex.
var entityFields = map[string]bool{
	"ID": true, "ProjectID": true, "ProjectIDs": true, "DefaultProjectID": true, "TaskID": true, "ParentTaskID": true,
	"ExecID": true, "ExecutionID": true, "AgentID": true, "AgentConfigID": true, "AgentDefinitionID": true,
	"DefaultAgentConfigID": true, "ScheduleID": true, "AlertID": true, "AutomationID": true,
}

func idField(name string) bool { return entityFields[name] }

// tagger prefixes explicit ID fields and ID keys of JSON settings fields.
// A value that merely looks like an ID is still ordinary text.
func tagger(serverID string) func(name, s string) string {
	return func(name, s string) string {
		switch {
		case s == "" || s == models.AllProjectsID:
			return s
		case (strings.HasSuffix(name, "ID") || strings.HasSuffix(name, "IDs")) && remoteid.IsEntityID(s):
			return remoteid.ID(serverID, s)
		case idField(name) && !strings.ContainsAny(s, " /{[\n:@"):
			return remoteid.ID(serverID, s)
		case jsonField(name):
			return tagJSON(serverID, s)
		}
		return s
	}
}

func jsonField(name string) bool { return strings.HasSuffix(name, "JSON") || name == "ChainConfig" }

// tagJSON prefixes hex IDs held by ID-named keys of a JSON object.
func tagJSON(serverID, s string) string {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var doc any
	if dec.Decode(&doc) != nil || dec.More() {
		return s
	}
	if !tagJSONValue(serverID, "", doc) {
		return s
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return s
	}
	return string(out)
}

func tagJSONValue(serverID, key string, v any) bool {
	changed := false
	switch v := v.(type) {
	case map[string]any:
		for k, e := range v {
			if id, ok := e.(string); ok && jsonIDKey(k) && remoteid.IsEntityID(id) {
				v[k] = remoteid.ID(serverID, id)
				changed = true
				continue
			}
			changed = tagJSONValue(serverID, k, e) || changed
		}
	case []any:
		for i, e := range v {
			if id, ok := e.(string); ok && jsonIDsKey(key) && remoteid.IsEntityID(id) {
				v[i] = remoteid.ID(serverID, id)
				changed = true
				continue
			}
			changed = tagJSONValue(serverID, key, e) || changed
		}
	}
	return changed
}

func jsonIDKey(k string) bool {
	return k == "id" || strings.HasSuffix(k, "_id") || strings.HasSuffix(k, "Id") || strings.HasSuffix(k, "ID")
}

func jsonIDsKey(k string) bool {
	return strings.HasSuffix(k, "_ids") || strings.HasSuffix(k, "Ids") || strings.HasSuffix(k, "IDs")
}

// Tag prefixes IDs in v, which must be a pointer.
func Tag(serverID string, v any) { walk(reflect.ValueOf(v), tagger(serverID), nil, "", 0) }

// argServer reports the single server referenced by args. Mixed servers fail.
func argServer(args []any) (server string, mixed bool) {
	var found string
	for _, a := range args {
		visitStrings(reflect.ValueOf(a), 0, func(s string) bool {
			if s == "" || !strings.Contains(s, "r-") {
				return true
			}
			id, ok := remoteid.FindServer(s)
			if !ok {
				return true
			}
			if found != "" && found != id {
				mixed = true
				return false
			}
			found = id
			return true
		})
		if mixed {
			return "", true
		}
	}
	return found, false
}

// aggregateArg returns the All projects or shared project ID in args, or "".
func aggregateArg(args []any) string {
	found := ""
	for _, a := range args {
		visitStrings(reflect.ValueOf(a), 0, func(s string) bool {
			if models.IsAggregateProject(s) {
				found = s
				return false
			}
			return true
		})
		if found != "" {
			break
		}
	}
	return found
}

func visitStrings(v reflect.Value, depth int, fn func(string) bool) bool {
	if depth > 3 || !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.String:
		return fn(v.String())
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return true
		}
		return visitStrings(v.Elem(), depth+1, fn)
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			if t.Field(i).IsExported() && !visitStrings(v.Field(i), depth+1, fn) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			return true
		}
		for i := 0; i < v.Len() && i < 1000; i++ {
			if !visitStrings(v.Index(i), depth+1, fn) {
				return false
			}
		}
	}
	return true
}

// Action is the request body of an "op":"action" socket command: a named
// handler operation that runs on the server owning its task or project.
type Action struct {
	Name string `json:"name"`
	Args []byte `json:"args"`
}

// StatusError is an action failure carrying an HTTP status for the caller.
type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string { return e.Message }

// Encode serializes one value for an action.
func Encode(v any) ([]byte, error) {
	return encodeValues([]reflect.Value{reflect.ValueOf(v)})
}

// Decode deserializes one action value into ptr.
func Decode(data []byte, ptr any) error {
	target := reflect.ValueOf(ptr).Elem()
	values, err := decodeValues(data, []reflect.Type{target.Type()})
	if err != nil {
		return err
	}
	target.Set(values[0])
	return nil
}

// EncodeResult answers an action. Results are scrubbed of secrets; canManage
// reports whether the caller manages a project.
func EncodeResult(v any, err error, canManage func(projectID string) bool) Result {
	if err != nil {
		var status *StatusError
		if errors.As(err, &status) {
			return Result{Code: "status:" + strconv.Itoa(status.Status), Error: status.Message}
		}
		code, msg := errorCode(err)
		return Result{Code: code, Error: msg}
	}
	rv := reflect.New(reflect.TypeOf(&v).Elem()).Elem()
	if v != nil {
		cp := reflect.New(reflect.TypeOf(v)).Elem()
		cp.Set(reflect.ValueOf(v))
		scrubValue(cp, canManage)
		rv = cp
	}
	encoded, encErr := encodeValues([]reflect.Value{rv})
	if encErr != nil {
		return Result{Error: "encoding result: " + encErr.Error()}
	}
	return Result{Values: encoded}
}

// TagIDs prefixes ID-named fields in v, which must be a pointer. Unlike Tag it
// does not inspect JSON settings fields. Free text is left intact.
func TagIDs(serverID string, v any) {
	walk(reflect.ValueOf(v), func(name, s string) string {
		if s == "" || s == models.AllProjectsID {
			return s
		}
		if idField(name) && !strings.ContainsAny(s, " /{[\n:@") {
			return remoteid.ID(serverID, s)
		}
		if (strings.HasSuffix(name, "ID") || strings.HasSuffix(name, "IDs")) && remoteid.IsEntityID(s) {
			return remoteid.ID(serverID, s)
		}
		return s
	}, nil, "", 0)
}

// ActionServer reports the saved server owning the IDs in v: a bare string
// argument or ID-named fields. Free text never routes an action.
func ActionServer(v any) (string, error) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return "", nil
	}
	cp := reflect.New(rv.Type()).Elem()
	cp.Set(rv)
	var server string
	var mixed bool
	walk(cp, func(name, s string) string {
		if name != "" && !idField(name) && !strings.HasSuffix(name, "ID") && !strings.HasSuffix(name, "IDs") {
			return s
		}
		if id, ok := remoteid.ServerOf(s); ok {
			mixed = mixed || server != "" && server != id
			server = id
		}
		return s
	}, nil, "", 0)
	if mixed {
		return "", errors.New("action references more than one server")
	}
	return server, nil
}

// Act runs a named action on serverID for the caller and decodes its result into out.
func (h *Hub) Act(ctx context.Context, serverID, name string, args, out any) error {
	conn := h.Get(callerUserID(ctx), serverID)
	if conn == nil {
		return ErrUnavailable
	}
	encoded, err := Encode(args)
	if err != nil {
		return err
	}
	argType := reflect.TypeOf(args)
	values, err := decodeValues(encoded, []reflect.Type{argType})
	if err != nil {
		return err
	}
	walk(values[0], func(_ string, s string) string { return remoteid.Strip(serverID, s) }, nil, "", 0)
	if encoded, err = encodeValues(values); err != nil {
		return err
	}
	res, err := conn.Act(ctx, Action{Name: name, Args: encoded})
	if err != nil {
		return err
	}
	if strings.HasPrefix(res.Code, "status:") {
		status, _ := strconv.Atoi(strings.TrimPrefix(res.Code, "status:"))
		return &StatusError{Status: status, Message: res.Error}
	}
	if err := resultError(res); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := Decode(res.Values, out); err != nil {
		return err
	}
	TagIDs(serverID, out)
	return nil
}
