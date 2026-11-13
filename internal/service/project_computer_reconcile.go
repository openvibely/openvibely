package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
)

// The helper is the durable source of running desktops. Every maintenance sweep
// rediscovers failed deletions, including after an application restart. No idle
// timeout applies to desktops whose task has been deleted.
func (s *ProjectComputerService) reconcileDeletedDesktops(ctx context.Context, id string) error {
	root := *s
	root.taskID = ""
	root.repo = s.projects.Computers()
	gate := root.lifecycle(id)
	gate.RLock()
	defer gate.RUnlock()
	state, err := root.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if state.Provider != root.provisioner.Provider() {
		return errors.New("computer provider differs from this installation")
	}
	conn, err := root.provisioner.Resolve(ctx, id)
	if err != nil {
		return err
	}
	raw, err := root.request(ctx, conn, "GET", "/desktop-sessions", nil)
	if err != nil {
		return err
	}
	var sessions map[string]bool
	if err = json.Unmarshal(raw, &sessions); err != nil {
		return err
	}
	// Read tasks AFTER the helper snapshot: desktops created while reconciliation
	// is running must not be mistaken for deleted tasks. On lookup failure stop none.
	tasks, err := root.repo.TaskIDsForDesktopCleanup(ctx, id)
	if err != nil {
		return err
	}
	existing := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		existing[ComputerDesktopID(task)] = true
	}
	names := []string{}
	for name, running := range sessions {
		if !running || existing[name] {
			continue
		}
		// Never interpolate arbitrary helper response paths into a DELETE request.
		if !strings.HasPrefix(name, "task-") || len(name) != 37 {
			continue
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(name, "task-")); err != nil {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var failures []error
	for _, name := range names {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, conn.Endpoint+"/desktops/"+name+"/", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+conn.Token)
		res, err := root.client.Do(req)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			failures = append(failures, errors.New("could not close deleted task desktop"))
		}
	}
	return errors.Join(failures...)
}
