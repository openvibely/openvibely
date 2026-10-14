package sqlitefixtures

import (
	"context"
	"strings"

	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
)

type ioCounter struct{ counter *testutil.SQLStatementCounter }

func NewIOCounter(counter *testutil.SQLStatementCounter) storefixture.IOCounter {
	return &ioCounter{counter: counter}
}
func (c *ioCounter) ObserveRequests(observer func(context.Context)) {
	if observer == nil {
		c.counter.SetObserver(nil)
		return
	}
	c.counter.SetObserver(func(ctx context.Context, _ string) { observer(ctx) })
}
func (c *ioCounter) Reset()                  { c.counter.Reset() }
func (c *ioCounter) SetEnabled(enabled bool) { c.counter.SetEnabled(enabled) }
func (c *ioCounter) Snapshot() storefixture.IOStats {
	statements := c.counter.Statements()
	stats := storefixture.IOStats{Requests: len(statements), ReadTextBytes: c.counter.SelectedTextBytes()}
	for _, statement := range statements {
		q := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if strings.HasPrefix(q, "insert ") || strings.HasPrefix(q, "update ") || strings.HasPrefix(q, "delete ") {
			stats.MutationRequests++
			if strings.Contains(q, "agents") || strings.Contains(q, "agent_lifecycle_hooks") {
				stats.AgentDeclarationMutationRequests++
			}
		}
		if strings.HasPrefix(q, "select ") {
			if strings.Contains(q, " from agents where coalesce(generated_status, 'user_edited') <> 'archived' order by name collate nocase asc, name asc, id asc limit ? offset ?") {
				stats.AgentPageReads++
			}
			if strings.Contains(q, " from agents where coalesce(generated_status, 'user_edited') <> 'archived' order by name asc") && !strings.Contains(q, " limit ? offset ?") {
				stats.UnboundedAgentListReads++
			}

			if strings.Contains(q, "from executions") {
				if strings.Contains(q, "order by started_at desc, rowid desc limit ?") {
					stats.ExecutionWindowReads++
				}
				if strings.Contains(q, "order by started_at asc") && !strings.Contains(q, " limit ") {
					stats.UnboundedExecutionHistoryReads++
				}
			}
			if strings.Contains(q, " from schedules ") {
				stats.ScheduleReads++
			}
			if strings.Contains(q, "from agents") {
				stats.AgentReads++
			}
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
			if strings.Contains(q, "from agent_configs order by is_default desc, name asc") || strings.Contains(q, "from agent_configs a ") && strings.Contains(q, "order by a.is_default desc, a.name asc") {
				stats.ModelCatalogReads++
			}
			if strings.HasPrefix(q, "select id, name, model, provider, preset_slug, reasoning_effort, is_default") {
				stats.ModelPickerReads++
			}
			if (strings.Contains(q, " from agent_configs order by is_default desc, name asc") || strings.Contains(q, "from agent_configs a ") && strings.Contains(q, "order by a.is_default desc, a.name asc")) && strings.Contains(q, "api_key") {
				stats.ModelFullCatalogReads++
			}
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
