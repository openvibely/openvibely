package remote

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/openvibely/openvibely/internal/cardcursor"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// Executor answers reads from hub clients on the owning server.
type Executor struct {
	Stores     persistence.Stores
	Projects   persistence.ProjectStore
	Authorizer *authz.Authorizer
}

// readableStores excludes credentials, identity and instance settings.
var readableStores = map[string]bool{
	"Agents": true, "Alerts": true, "Attachments": true, "Automations": true, "ChannelTargets": true,
	"ChatAttachments": true, "ChatInputRequests": true, "Executions": true,
	"Insights": true, "Lifecycle": true, "LLMConfigs": true, "Projects": true, "ReviewComments": true,
	"Schedules": true, "SkillAnalytics": true, "TaskCommitStats": true, "TaskGoals": true,
	"TaskPullRequests": true, "Tasks": true, "ThreadInputs": true, "Upcoming": true, "Usage": true,
	"Webhooks": true, "Workers": true,
}

func (e *Executor) Run(ctx context.Context, read Read) Result {
	values, err := e.run(ctx, read)
	if err != nil {
		code, msg := errorCode(err)
		return Result{Code: code, Error: msg}
	}
	encoded, err := encodeValues(values)
	if err != nil {
		return Result{Error: "encoding result: " + err.Error()}
	}
	version := 0
	if read.Page != nil {
		version = 1
	}
	return Result{Values: encoded, CursorVersion: version}
}

func (e *Executor) run(ctx context.Context, read Read) ([]reflect.Value, error) {
	if read.Page != nil {
		if !cursorRead(read.Page, read.Store, read.Method) || len(read.Page.Token.Sources) > 1 {
			return nil, cardcursor.ErrInvalid
		}
		ctx = cardcursor.With(ctx, read.Page)
	}
	if read.Scope != nil {
		ctx = models.WithScopeProjects(ctx, read.Scope)
	}
	if !routable(read.Store) || !IsRead(read.Store, read.Method) {
		return nil, ErrUnsupported
	}
	if _, known := paramNames[read.Store][read.Method]; !known {
		return nil, ErrUnsupported
	}
	store := reflect.ValueOf(e.Stores).FieldByName(read.Store)
	if read.Store == "Projects" {
		store = reflect.ValueOf(&e.Projects).Elem()
	}
	if !store.IsValid() || store.IsNil() {
		return nil, ErrUnsupported
	}
	store = store.Elem()
	method := store.MethodByName(read.Method)
	if !method.IsValid() {
		return nil, ErrUnsupported
	}
	mt := method.Type()
	if mt.NumIn() == 0 || mt.In(0) != reflect.TypeOf((*context.Context)(nil)).Elem() || mt.IsVariadic() {
		return nil, ErrUnsupported
	}
	types := make([]reflect.Type, mt.NumIn()-1)
	for i := range types {
		types[i] = mt.In(i + 1)
	}
	args, err := decodeValues(read.Args, types)
	if err != nil {
		return nil, fmt.Errorf("decoding arguments: %w", err)
	}
	self := callerUserID(ctx)
	names := paramNames[read.Store][read.Method]
	for i, a := range args {
		if i < len(names) && userParam(names[i]) && a.Kind() == reflect.String {
			// A client can only ask about its own signed-in user.
			a.SetString(self)
			continue
		}
		walk(a, func(_ string, s string) string {
			if s == Self {
				return self
			}
			return s
		}, nil, "", 0)
	}
	owners := newOwnerSet()
	for i, a := range args {
		if i < len(names) {
			owners.addParam(read.Store, names[i], a)
		}
		owners.collect(a, 0)
	}
	if i, j := indexOf(names, "scopeType"), indexOf(names, "scopeID"); i >= 0 && j >= 0 {
		owners.addScope(args[i].String(), args[j].String())
	}
	in := append([]reflect.Value{reflect.ValueOf(ctx)}, args...)
	out := method.Call(in)
	if errV := out[len(out)-1]; !errV.IsNil() {
		return nil, errV.Interface().(error)
	}
	out = out[:len(out)-1]
	// Results may be shared with store caches; scrub and filter a private copy.
	encoded, err := encodeValues(out)
	if err != nil {
		return nil, err
	}
	outTypes := make([]reflect.Type, len(out))
	for i, v := range out {
		outTypes[i] = v.Type()
	}
	if out, err = decodeValues(encoded, outTypes); err != nil {
		return nil, err
	}
	decided := map[string]error{}
	if err := e.authorize(ctx, owners, decided); err != nil {
		return nil, err
	}
	// Project data with no identifiable owner is refused rather than returned unchecked.
	ownerRequired := !ownerless[read.Store] && owners.empty()
	for i, v := range out {
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() != reflect.Uint8 {
			// Lists may span projects; drop rows the caller cannot view.
			kept := reflect.MakeSlice(v.Type(), 0, v.Len())
			for j := 0; j < v.Len(); j++ {
				row := newOwnerSet()
				row.collect(v.Index(j), 0)
				if ownerRequired && row.empty() {
					continue
				}
				if e.authorize(ctx, row, decided) == nil {
					kept = reflect.Append(kept, v.Index(j))
				}
			}
			out[i] = kept
			continue
		}
		row := newOwnerSet()
		row.collect(v, 0)
		if ownerRequired && row.empty() {
			return nil, persistence.ErrProjectDenied
		}
		if err := e.authorize(ctx, row, decided); err != nil {
			return nil, err
		}
	}
	managed := map[string]bool{}
	canManage := func(projectID string) bool {
		ok, seen := managed[projectID]
		if !seen {
			ok = e.Authorizer.Require(ctx, projectID, authz.Manage) == nil
			managed[projectID] = ok
		}
		return ok
	}
	for _, v := range out {
		scrubValue(v, canManage)
	}
	return out, nil
}

// scrubValue clears secret-named fields in a copy of v.
func scrubValue(v reflect.Value, canManage func(projectID string) bool) {
	if !v.CanSet() && v.Kind() != reflect.Slice {
		return
	}
	if canManage == nil {
		canManage = func(string) bool { return false }
	}
	walk(v, func(_ string, s string) string { return s }, canManage, "", 0)
}

// ownerless stores hold instance-wide data rather than project data.
var ownerless = map[string]bool{
	"Agents": true, "LLMConfigs": true, "Workers": true, "Projects": true, "ProjectAccess": true,
}

type ownerSet struct {
	projects, tasks, execs map[string]bool
	n                      int
}

func newOwnerSet() ownerSet {
	return ownerSet{projects: map[string]bool{}, tasks: map[string]bool{}, execs: map[string]bool{}}
}

func (o *ownerSet) empty() bool { return len(o.projects)+len(o.tasks)+len(o.execs) == 0 }

// addParam records a named argument's owner. Bare id/ids name the store's own entity.
func (o *ownerSet) addParam(store, name string, v reflect.Value) {
	var target map[string]bool
	switch strings.ToLower(name) {
	case "projectid", "projectids":
		target = o.projects
	case "taskid", "taskids", "parenttaskid", "parentid":
		target = o.tasks
	case "executionid", "executionids", "execid", "execids":
		target = o.execs
	case "id", "ids":
		switch store {
		case "Executions":
			target = o.execs
		case "Tasks":
			target = o.tasks
		}
	}
	if target == nil {
		return
	}
	switch {
	case v.Kind() == reflect.String && v.String() != "":
		target[v.String()] = true
	case v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.String:
		for i := 0; i < v.Len(); i++ {
			if id := v.Index(i).String(); id != "" {
				target[id] = true
			}
		}
	}
}

// addScope records a conversation or checkpoint scope's owner.
func (o *ownerSet) addScope(scopeType, scopeID string) {
	switch {
	case scopeID == "":
	case scopeType == "task":
		o.tasks[scopeID] = true
	case scopeType != "":
		// chat_project scopes are project IDs; per-user All Projects scopes never match a project and are denied.
		o.projects[scopeID] = true
	}
}

func indexOf(names []string, name string) int {
	for i, n := range names {
		if n == name {
			return i
		}
	}
	return -1
}

func (o *ownerSet) collect(v reflect.Value, depth int) {
	if depth > 8 || !v.IsValid() || o.n > 100000 {
		return
	}
	o.n++
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			o.collect(v.Elem(), depth+1)
		}
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			return
		}
		for i := 0; i < v.Len(); i++ {
			o.collect(v.Index(i), depth+1)
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			o.collect(iter.Value(), depth+1)
		}
	case reflect.Struct:
		str := func(name string) string {
			f := v.FieldByName(name)
			if f.IsValid() && f.Kind() == reflect.String {
				return f.String()
			}
			if f.IsValid() && f.Kind() == reflect.Pointer && !f.IsNil() && f.Elem().Kind() == reflect.String {
				return f.Elem().String()
			}
			return ""
		}
		switch {
		case str("ProjectID") != "":
			o.projects[str("ProjectID")] = true
		case str("TaskID") != "":
			o.tasks[str("TaskID")] = true
		case str("ExecutionID") != "":
			o.execs[str("ExecutionID")] = true
		case str("ScopeType") != "":
			o.addScope(str("ScopeType"), str("ScopeID"))
		}
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			if t.Field(i).IsExported() {
				o.collect(v.Field(i), depth+1)
			}
		}
	}
}

func (e *Executor) authorize(ctx context.Context, o ownerSet, decided map[string]error) error {
	for id := range o.execs {
		taskID, err := e.Stores.Executions.GetExecutionTaskID(ctx, id)
		if err != nil || taskID == "" {
			return persistence.ErrProjectDenied
		}
		o.tasks[taskID] = true
	}
	for id := range o.tasks {
		task, err := e.Stores.Tasks.GetThreadRenderMetadata(ctx, id)
		if err != nil || task == nil || task.ProjectID == "" {
			return persistence.ErrProjectDenied
		}
		o.projects[task.ProjectID] = true
	}
	for id := range o.projects {
		if id == "" {
			continue
		}
		if id == models.AllProjectsID {
			if p, ok := models.PrincipalFromContext(ctx); !ok || p.UserID == "" {
				return persistence.ErrProjectDenied
			}
			continue
		}
		err, ok := decided[id]
		if !ok {
			err = e.Authorizer.Require(ctx, id, authz.View)
			decided[id] = err
		}
		if err != nil {
			return err
		}
	}
	return nil
}
