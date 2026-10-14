package sqlitefixtures

import (
	"strings"

	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
)

type ioCounter struct{ counter *testutil.SQLStatementCounter }

func NewIOCounter(counter *testutil.SQLStatementCounter) storefixture.IOCounter {
	return &ioCounter{counter: counter}
}
func (c *ioCounter) Reset()                  { c.counter.Reset() }
func (c *ioCounter) SetEnabled(enabled bool) { c.counter.SetEnabled(enabled) }
func (c *ioCounter) Snapshot() storefixture.IOStats {
	statements := c.counter.Statements()
	stats := storefixture.IOStats{Requests: len(statements), ReadTextBytes: c.counter.SelectedTextBytes()}
	for _, statement := range statements {
		q := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if strings.HasPrefix(q, "select ") {
			if strings.Contains(q, " from projects") {
				stats.ProjectReads++
			}
			if strings.Contains(q, " from alerts") {
				stats.AlertReads++
			}
			if strings.Contains(q, "from executions") && strings.Contains(q, "count(*)") {
				stats.ExecutionCountReads++
			}
			if strings.Contains(q, "from executions") && strings.Contains(q, "order by started_at asc, rowid asc limit ? offset ?") {
				stats.ExecutionPageReads++
			}
			if strings.Contains(q, "from agent_configs") {
				if strings.HasPrefix(q, "select id, name, provider, model, is_default, auth_method, max_workers, worker_timeout") {
					if strings.Contains(q, "where name = ? collate nocase") {
						stats.ModelRuntimeLookupReads++
					} else if strings.Contains(q, "order by is_default desc, name asc") {
						stats.ModelRuntimeListReads++
					}
				}
				if strings.HasPrefix(q, "select id, name, provider, model, is_default, auto_start_tasks, auth_method from agent_configs") {
					stats.ModelCreationSelectionReads++
				}
			}
		}
		if strings.HasPrefix(q, "select ") && strings.Contains(q, "from agent_configs") {
			stats.ModelReads++
			projection := strings.Split(q, " from agent_configs")[0]
			if projection == "select id, name, provider, model, is_default, auth_method" || strings.HasPrefix(projection, "select id, name, provider, model, auth_method, is_default,") {
				stats.ModelSelectionReads++
			}
		}
		if strings.Contains(q, "substr(t.prompt") {
			stats.TaskContextReads++
		}
		if strings.Contains(q, "update tasks ") || strings.HasPrefix(q, "insert into tasks ") || strings.HasPrefix(q, "delete from tasks ") {
			stats.TaskWrites++
		}
		if strings.HasPrefix(q, "update executions ") || strings.HasPrefix(q, "insert into executions ") || strings.HasPrefix(q, "delete from executions ") {
			stats.ExecutionWrites++
		}
		if strings.HasPrefix(q, "select ") && strings.Contains(q, "from slack_authorized_users") {
			stats.SlackAuthorizationReads++
		}
		if strings.HasPrefix(q, "select ") && strings.Contains(q, "from app_settings where key =") {
			stats.SettingsSingleReads++
		}
		if strings.HasPrefix(q, "select ") && strings.Contains(q, "from app_settings where key in") {
			stats.SettingsSnapshotReads++
		}
		if strings.HasPrefix(q, "select ") && strings.Contains(q, "from projects order by") {
			stats.ProjectCatalogReads++
		}
		if strings.HasPrefix(q, "select ") && strings.Contains(q, "from telegram_authorized_users") {
			stats.TelegramAuthorizationReads++
		}
		if strings.HasPrefix(q, "select ") && strings.Contains(q, "from github_authorized_actors") {
			stats.GitHubAuthorizationReads++
		}
		if strings.Contains(q, "from executions where task_id = ?") && strings.Contains(q, "prompt_sent") && strings.Contains(q, "output") {
			stats.ExecutionHistoryReads++
		}
	}
	return stats
}
