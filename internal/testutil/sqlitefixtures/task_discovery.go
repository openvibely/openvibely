package sqlitefixtures

import (
	"context"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/testutil/storefixture"
)

func (f *fixtures) SeedTaskDiscoveryWorkload(ctx context.Context, workload storefixture.TaskDiscoveryWorkload) error {
	if workload.Count < 0 || workload.PromptBytes < 0 || workload.ConfigurationBytes < 0 {
		return fmt.Errorf("negative discovery workload size")
	}
	if workload.Count == 0 {
		return nil
	}
	prompt := strings.Repeat("p", workload.PromptBytes)
	chainConfig := `{"payload":"` + strings.Repeat("c", workload.ConfigurationBytes) + `"}`
	swarmConfig := `{"payload":"` + strings.Repeat("s", workload.ConfigurationBytes) + `"}`
	_, err := f.db.ExecContext(ctx, `
		WITH RECURSIVE seq(n) AS (
			SELECT 1
			UNION ALL
			SELECT n + 1 FROM seq WHERE n < ?
		)
		INSERT INTO tasks
			(id, project_id, title, category, priority, status, prompt, chain_config, swarm_config, updated_at)
		SELECT
			? || '-task-discovery-benchmark-' || printf('%05d', n),
			?,
			'Discovery benchmark task ' || printf('%05d', n),
			CASE WHEN n % 20 = 0 THEN 'chat' ELSE 'active' END,
			(n % 4) + 1,
			'pending',
			?, ?, ?,
			datetime('2026-01-01 00:00:00', '+' || n || ' seconds')
		FROM seq`, workload.Count, workload.ProjectID, workload.ProjectID, prompt, chainConfig, swarmConfig)
	return err
}
