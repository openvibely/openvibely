package remote

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/cardcursor"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
)

// Router sends store calls carrying remote IDs to their server.
type Router struct {
	Hub *Hub
}

// routable stores hold project data owned by one server. Others (settings,
// credentials, identity) stay local even when a value names a remote entity.
func routable(store string) bool { return readableStores[store] || store == "ProjectAccess" }

func userParam(name string) bool {
	n := strings.ToLower(name)
	return n == "userid" || n == "principalid" || n == "actorid" || n == "audienceuserid"
}

func callerUserID(ctx context.Context) string {
	if p, ok := models.PrincipalFromContext(ctx); ok && p.UserID != "" {
		return p.UserID
	}
	return models.BootstrapUserID
}

type serverContextKey struct{}

// WithServer routes calls without remote IDs to serverID (for example, a remote
// project's selector lists).
func WithServer(ctx context.Context, serverID string) context.Context {
	return context.WithValue(ctx, serverContextKey{}, serverID)
}

func contextServer(ctx context.Context) string {
	s, _ := ctx.Value(serverContextKey{}).(string)
	return s
}

// Refuse reports whether a non-transmittable call targets a remote server.
func (r *Router) Refuse(ctx context.Context, store, method string, args []any) bool {
	if !routable(store) {
		return false
	}
	ids := routingIDs(store, method, args)
	server, mixed := argServer(ids)
	return mixed || server != "" || (len(ids) == 0 && contextServer(ctx) != "")
}

// Remote runs the call remotely when args (or ctx) name a remote server, or
// merges every connected server into an All Projects read. It reports whether
// the results were set; otherwise the caller runs locally.
func (r *Router) Remote(ctx context.Context, store, method string, args []any, local func() []any, results ...any) bool {
	if !routable(store) {
		return false
	}
	ids := routingIDs(store, method, args)
	server, mixed := argServer(ids)
	if server == "" && len(ids) == 0 && store != "Projects" && store != "ProjectAccess" {
		// Explicit resource IDs (including local and aggregate IDs) take precedence
		// over a page's remote context.
		// Project lists drive local navigation; a remote page still lists local projects.
		server = contextServer(ctx)
	}
	errPtr := results[len(results)-1].(*error)
	if mixed {
		*errPtr = ErrUnsupported
		return true
	}
	if server == "" {
		if _, ok := models.PrincipalFromContext(ctx); !ok {
			return false
		}
		aggregate := aggregateArg(ids)
		if local == nil || aggregate == "" || !IsRead(store, method) || r.Hub == nil || !mergeable(results) {
			return false
		}
		servers := r.Hub.Servers(callerUserID(ctx))
		var scopes map[string][]string
		if models.IsSharedProjectID(aggregate) {
			servers, scopes = sharedScopes(ctx, servers)
		}
		if s := cardcursor.From(ctx); cursorRead(s, store, method) {
			available := map[string]bool{}
			for _, id := range servers {
				available[id] = true
			}
			if len(s.Token.Sources) > 0 {
				servers = nil
				for id := range s.Token.Sources {
					if id == "" {
						continue
					}
					if !available[id] {
						*errPtr = ErrUnavailable
						return true
					}
					servers = append(servers, id)
				}
			} else {
				s.Token.Sources[""] = nil
				for _, id := range servers {
					s.Token.Sources[id] = nil
				}
			}
			sort.Strings(servers)
		}
		if len(servers) == 0 {
			return false
		}
		r.fanOut(ctx, store, method, args, servers, scopes, local, results)
		return true
	}
	if !IsRead(store, method) {
		*errPtr = ErrUnsupported
		return true
	}
	*errPtr = r.call(ctx, server, store, method, args, nil, results)
	return true
}

// Read runs one store read on serverID, regardless of the arguments' IDs.
func (h *Hub) Read(ctx context.Context, serverID, store, method string, args []any, results ...any) error {
	if !routable(store) || !IsRead(store, method) {
		return ErrUnsupported
	}
	return (&Router{Hub: h}).call(ctx, serverID, store, method, args, nil, results)
}

// sharedScopes keeps the servers holding copies of the context's shared
// project, with each server's own IDs for those copies.
func sharedScopes(ctx context.Context, servers []string) ([]string, map[string][]string) {
	members, _ := models.ScopeProjects(ctx)
	scopes := map[string][]string{}
	for _, id := range members {
		if server, local, ok := remoteid.Split(id); ok {
			scopes[server] = append(scopes[server], local)
		}
	}
	var kept []string
	for _, server := range servers {
		if len(scopes[server]) > 0 {
			kept = append(kept, server)
		}
	}
	return kept, scopes
}

func (r *Router) call(ctx context.Context, server, store, method string, args []any, scope []string, results []any) error {
	conn := r.Hub.Get(callerUserID(ctx), server)
	if conn == nil {
		return ErrUnavailable
	}
	payload, err := prepareArgs(store, method, server, args, scope != nil)
	if err != nil {
		return err
	}
	read := Read{Store: store, Method: method, Args: payload, Scope: scope}
	if s := cardcursor.From(ctx); cursorRead(s, store, method) {
		read.Page = s.ForSource(server)
	}
	res, err := conn.Call(ctx, read)
	if err != nil {
		return err
	}
	if read.Page != nil && res.CursorVersion != 1 && res.Error == "" {
		return ErrUnsupported
	}
	if err := resultError(res); err != nil {
		return err
	}
	types := make([]reflect.Type, len(results)-1)
	for i := range types {
		types[i] = reflect.TypeOf(results[i]).Elem()
	}
	values, err := decodeValues(res.Values, types)
	if err != nil {
		return err
	}
	for i, v := range values {
		tagReadResult(server, store, method, i, v)
		reflect.ValueOf(results[i]).Elem().Set(v)
	}
	return nil
}

// prepareArgs deep-copies args, strips this server's prefix and replaces the
// caller's user ID, which means nothing on the other server. A scoped read
// sends its shared project as All projects, narrowed by the read's scope.
func prepareArgs(store, method, server string, args []any, scoped bool) ([]byte, error) {
	values := make([]reflect.Value, len(args))
	types := make([]reflect.Type, len(args))
	for i, a := range args {
		values[i] = reflect.ValueOf(a)
		types[i] = reflect.TypeOf(a)
		if types[i] == nil {
			types[i] = reflect.TypeOf((*any)(nil)).Elem()
			values[i] = reflect.Zero(types[i])
		}
	}
	encoded, err := encodeValues(values)
	if err != nil {
		return nil, err
	}
	copies, err := decodeValues(encoded, types)
	if err != nil {
		return nil, err
	}
	names := paramNames[store][method]
	strip := func(name string, s string) string {
		if !resourceIDName(name) {
			return s
		}
		if scoped && models.IsSharedProjectID(s) {
			return models.AllProjectsID
		}
		return remoteid.Strip(server, s)
	}
	for i, v := range copies {
		if i < len(names) && userParam(names[i]) && v.Kind() == reflect.String {
			v.SetString(Self)
			continue
		}
		name := ""
		if i < len(names) {
			name = names[i]
		}
		walk(v, strip, nil, name, 0)
	}
	return encodeValues(copies)
}

func mergeable(results []any) bool {
	if len(results) != 2 && len(results) != 3 {
		return false
	}
	switch reflect.TypeOf(results[0]).Elem().Kind() {
	case reflect.Slice, reflect.Map, reflect.Int, reflect.Int64:
		return len(results) == 2 || reflect.TypeOf(results[1]).Elem().Kind() == reflect.Bool || reflect.TypeOf(results[1]).Elem().Kind() == reflect.Int
	}
	return false
}

// fanOut merges local and remote All Projects reads. Unreachable servers are
// skipped after a short wait so one offline server cannot stall a page.
func (r *Router) fanOut(ctx context.Context, store, method string, args []any, servers []string, scopes map[string][]string, local func() []any, results []any) {
	type part struct {
		values []any
		err    error
	}
	parts := make([]part, len(servers))
	var wg sync.WaitGroup
	for i, server := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			ptrs := make([]any, len(results))
			for j, res := range results {
				ptrs[j] = reflect.New(reflect.TypeOf(res).Elem()).Interface()
			}
			err := r.call(callCtx, server, store, method, args, scopes[server], ptrs)
			parts[i] = part{values: ptrs, err: err}
		}()
	}
	out := local()
	wg.Wait()
	for i, v := range out {
		if v == nil {
			continue
		}
		reflect.ValueOf(results[i]).Elem().Set(reflect.ValueOf(v))
	}
	if *(results[len(results)-1].(*error)) != nil {
		return
	}
	for _, p := range parts {
		if p.err != nil {
			if cursorRead(cardcursor.From(ctx), store, method) {
				*(results[len(results)-1].(*error)) = p.err
				return
			}
			continue
		}
		for j := 0; j < len(results)-1; j++ {
			mergeInto(reflect.ValueOf(results[j]).Elem(), reflect.ValueOf(p.values[j]).Elem())
		}
	}
}

func mergeInto(dst, src reflect.Value) {
	switch dst.Kind() {
	case reflect.Slice:
		dst.Set(reflect.AppendSlice(dst, src))
	case reflect.Map:
		if src.IsNil() {
			return
		}
		if dst.IsNil() {
			dst.Set(reflect.MakeMap(dst.Type()))
		}
		iter := src.MapRange()
		for iter.Next() {
			cur := dst.MapIndex(iter.Key())
			if cur.IsValid() && cur.CanInt() {
				dst.SetMapIndex(iter.Key(), reflect.ValueOf(cur.Int()+iter.Value().Int()).Convert(cur.Type()))
				continue
			}
			dst.SetMapIndex(iter.Key(), iter.Value())
		}
	case reflect.Int, reflect.Int64:
		dst.SetInt(dst.Int() + src.Int())
	case reflect.Bool:
		dst.SetBool(dst.Bool() || src.Bool())
	}
}

// Cursor state applies only to the requested card query, not nested selector or
// maintenance reads which happen during the same page render.
func cursorRead(s *cardcursor.State, store, method string) bool {
	if s == nil {
		return false
	}
	switch store + "." + method {
	case "Automations.ListPortfolioCardsPageFiltered":
		return s.Kind == "automations"
	case "Alerts.ListFilteredSummaries":
		return s.Kind == "alerts"
	case "Agents.ListPageFiltered":
		return s.Kind == "agents"
	case "LLMConfigs.ListCardsPageFiltered":
		return s.Kind == "models"
	case "Webhooks.ListCardsByProjectPageFiltered":
		return s.Kind == "webhooks"
	}
	return false
}

// RoutingServer identifies the explicit server scope for request-local caches.
func RoutingServer(ctx context.Context) string { return contextServer(ctx) }
