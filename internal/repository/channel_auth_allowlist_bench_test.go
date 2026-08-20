package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/database"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

const (
	authAllowlistBenchProjectID = "auth-allowlist-bench-project"
	authAllowlistBenchRows      = 25000
)

var authAllowlistListIndexes = map[string]string{
	"telegram": "idx_telegram_auth_list_covering",
	"slack":    "idx_slack_auth_list_covering",
	"discord":  "idx_discord_auth_list_covering",
	"email":    "idx_email_auth_list_covering",
}

var authAllowlistListQueries = map[string]string{
	"telegram": telegramAuthorizedUsersListQuery,
	"slack":    authorizedUsersListQuery("slack_authorized_users", "slack_user_id"),
	"discord":  authorizedUsersListQuery("discord_authorized_users", "discord_user_id"),
	"email":    authorizedUsersListQuery("email_authorized_senders", "email_address"),
}

func newAuthAllowlistBenchDB(tb testing.TB) *sql.DB {
	tb.Helper()
	db, err := database.New(":memory:")
	if err != nil {
		tb.Fatalf("opening auth allowlist benchmark database: %v", err)
	}
	tb.Cleanup(func() { db.Close() })
	return db
}

func seedAuthAllowlistFixture(tb testing.TB, db *sql.DB, rowsPerChannel int) {
	tb.Helper()
	if _, err := db.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES (?, 'Auth Allowlist Bench', '', '')`, authAllowlistBenchProjectID); err != nil {
		tb.Fatalf("insert bench project: %v", err)
	}

	statements := []struct {
		label string
		query string
	}{
		{
			label: "slack",
			query: `WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?)
				INSERT INTO slack_authorized_users (project_id, slack_user_id, display_name, added_at, added_by)
				SELECT ?, 'U' || printf('%07d', n), 'Slack ' || n,
				       datetime('2020-01-01 00:00:00', '+' || ((n * 7919) % ?) || ' seconds'), 'bench'
				FROM seq`,
		},
		{
			label: "discord",
			query: `WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?)
				INSERT INTO discord_authorized_users (project_id, discord_user_id, display_name, added_at, added_by)
				SELECT ?, '900000000000' || printf('%06d', n), 'Discord ' || n,
				       datetime('2020-01-01 00:00:00', '+' || ((n * 7919) % ?) || ' seconds'), 'bench'
				FROM seq`,
		},
		{
			label: "email",
			query: `WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?)
				INSERT INTO email_authorized_senders (project_id, email_address, display_name, added_at, added_by)
				SELECT ?, 'user' || printf('%07d', n) || '@example.com', 'Email ' || n,
				       datetime('2020-01-01 00:00:00', '+' || ((n * 7919) % ?) || ' seconds'), 'bench'
				FROM seq`,
		},
		{
			label: "telegram",
			query: `WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?)
				INSERT INTO telegram_authorized_users (project_id, telegram_user_id, telegram_username, display_name, added_at, added_by)
				SELECT ?, n, 'tg' || printf('%07d', n), 'Telegram ' || n,
				       datetime('2020-01-01 00:00:00', '+' || ((n * 7919) % ?) || ' seconds'), 'bench'
				FROM seq`,
		},
	}

	tx, err := db.Begin()
	if err != nil {
		tb.Fatalf("begin auth allowlist fixture: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range statements {
		if _, err := tx.Exec(stmt.query, rowsPerChannel, authAllowlistBenchProjectID, rowsPerChannel); err != nil {
			tb.Fatalf("insert %s auth fixture: %v", stmt.label, err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit auth allowlist fixture: %v", err)
	}

	for table, want := range map[string]int{
		"slack_authorized_users":    rowsPerChannel,
		"discord_authorized_users":  rowsPerChannel,
		"email_authorized_senders":  rowsPerChannel,
		"telegram_authorized_users": rowsPerChannel,
	} {
		var got int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got); err != nil {
			tb.Fatalf("count %s fixture rows: %v", table, err)
		}
		if got != want {
			tb.Fatalf("%s fixture rows = %d, want %d", table, got, want)
		}
	}
}

func setAuthAllowlistListIndexes(tb testing.TB, db *sql.DB, candidate bool) {
	tb.Helper()
	for _, indexName := range authAllowlistListIndexes {
		if _, err := db.Exec(`DROP INDEX IF EXISTS ` + indexName); err != nil {
			tb.Fatalf("drop %s: %v", indexName, err)
		}
	}
	if !candidate {
		return
	}
	statements := []string{
		`CREATE INDEX IF NOT EXISTS idx_telegram_auth_list_covering ON telegram_authorized_users(added_at, id, project_id, telegram_user_id, telegram_username, display_name, added_by)`,
		`CREATE INDEX IF NOT EXISTS idx_slack_auth_list_covering ON slack_authorized_users(added_at, id, project_id, slack_user_id, display_name, added_by)`,
		`CREATE INDEX IF NOT EXISTS idx_discord_auth_list_covering ON discord_authorized_users(added_at, id, project_id, discord_user_id, display_name, added_by)`,
		`CREATE INDEX IF NOT EXISTS idx_email_auth_list_covering ON email_authorized_senders(added_at, id, project_id, email_address, display_name, added_by)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			tb.Fatalf("create auth allowlist list index: %v", err)
		}
	}
}

func authAllowlistExplain(tb testing.TB, db *sql.DB, query string, args ...any) string {
	tb.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		tb.Fatalf("explain auth allowlist query plan: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			tb.Fatalf("scan explain row: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		tb.Fatalf("explain rows: %v", err)
	}
	return fmt.Sprintf("%v", details)
}

func TestChannelAuthAllowlistListQueriesUseCoveringOrderIndexes(t *testing.T) {
	db := newAuthAllowlistBenchDB(t)
	seedAuthAllowlistFixture(t, db, 1500)

	setAuthAllowlistListIndexes(t, db, false)
	for channel, query := range authAllowlistListQueries {
		plan := authAllowlistExplain(t, db, query)
		if !strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
			t.Fatalf("%s baseline plan = %s, want temporary ORDER BY sort", channel, plan)
		}
	}

	setAuthAllowlistListIndexes(t, db, true)
	for channel, query := range authAllowlistListQueries {
		plan := authAllowlistExplain(t, db, query)
		if !strings.Contains(plan, "USING COVERING INDEX "+authAllowlistListIndexes[channel]) {
			t.Fatalf("%s candidate plan = %s, want covering list index %s", channel, plan, authAllowlistListIndexes[channel])
		}
		if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
			t.Fatalf("%s candidate plan = %s, want no temporary ORDER BY sort", channel, plan)
		}
	}

	assertAuthAllowlistRepositoryResults(t, db, 1500)
}

func TestChannelAuthCountByProjectRemainsSystemLevel(t *testing.T) {
	db := newAuthAllowlistBenchDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO projects (id, name, description, repo_path) VALUES ('auth-count-project-a', 'A', '', ''), ('auth-count-project-b', 'B', '', '')`); err != nil {
		t.Fatalf("insert projects: %v", err)
	}
	slack := NewSlackAuthRepo(db)
	discord := NewDiscordAuthRepo(db)
	email := NewEmailAuthRepo(db)
	telegram := NewTelegramAuthRepo(db)

	for i, projectID := range []string{"auth-count-project-a", "auth-count-project-b"} {
		require.NoError(t, slack.Create(ctx, &models.SlackAuthorizedUser{ProjectID: projectID, SlackUserID: "U-" + projectID, AddedBy: "test"}))
		require.NoError(t, discord.Create(ctx, &models.DiscordAuthorizedUser{ProjectID: projectID, DiscordUserID: "D-" + projectID, AddedBy: "test"}))
		require.NoError(t, email.Create(ctx, &models.EmailAuthorizedSender{ProjectID: projectID, EmailAddress: projectID + "@example.com", AddedBy: "test"}))
		require.NoError(t, telegram.Create(ctx, &models.TelegramAuthorizedUser{ProjectID: projectID, TelegramUserID: int64(1000 + i), TelegramUsername: projectID, AddedBy: "test"}))
	}

	for _, projectID := range []string{"auth-count-project-a", "auth-count-project-b", "unrelated-project"} {
		got, err := slack.CountByProject(ctx, projectID)
		require.NoError(t, err)
		require.Equal(t, 2, got, "slack count must remain system-level for %s", projectID)
		got, err = discord.CountByProject(ctx, projectID)
		require.NoError(t, err)
		require.Equal(t, 2, got, "discord count must remain system-level for %s", projectID)
		got, err = email.CountByProject(ctx, projectID)
		require.NoError(t, err)
		require.Equal(t, 2, got, "email count must remain system-level for %s", projectID)
		got, err = telegram.CountByProject(ctx, projectID)
		require.NoError(t, err)
		require.Equal(t, 2, got, "telegram count must remain system-level for %s", projectID)
	}
}

func TestChannelStatusAuthCountQueriesAvoidOrderedFullListPlans(t *testing.T) {
	db := newAuthAllowlistBenchDB(t)
	seedAuthAllowlistFixture(t, db, 1500)

	countQueries := map[string]string{
		"slack":    `SELECT COUNT(*) FROM slack_authorized_users`,
		"discord":  `SELECT COUNT(*) FROM discord_authorized_users`,
		"email":    `SELECT COUNT(*) FROM email_authorized_senders`,
		"telegram": `SELECT COUNT(*) FROM telegram_authorized_users`,
	}
	for channel, query := range countQueries {
		require.NotContains(t, strings.ToUpper(query), "ORDER BY", "%s status count query must not be an ordered full-list query", channel)
		plan := authAllowlistExplain(t, db, query)
		require.NotContains(t, plan, "USE TEMP B-TREE FOR ORDER BY", "%s status count plan must not sort", channel)
	}
}

func seedChannelStatusTargetFixture(tb testing.TB, db *sql.DB, projectID string, rows int) {
	tb.Helper()
	if _, err := db.Exec(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?)
		INSERT INTO channel_targets (id, project_id, platform, target_kind, name, target_id, thread_id, is_home, default_subject)
		SELECT 'status-target-' || printf('%07d', n), ?,
		       CASE n % 4 WHEN 0 THEN 'slack' WHEN 1 THEN 'telegram' WHEN 2 THEN 'discord' ELSE 'email' END,
		       CASE n % 4 WHEN 1 THEN 'chat' WHEN 3 THEN 'email' WHEN 2 THEN CASE n % 8 WHEN 2 THEN 'user' ELSE 'channel' END ELSE CASE n % 8 WHEN 0 THEN 'user' ELSE 'channel' END END,
		       CASE WHEN n % 3 = 0 THEN 'name-' || printf('%07d', n) ELSE '' END,
		       'target-' || printf('%07d', n),
		       CASE WHEN n % 5 = 0 THEN 'thread-' || printf('%07d', n) ELSE '' END,
		       CASE WHEN n IN (1, 2, 3, 4) THEN 1 ELSE 0 END,
		       'subject'
		FROM seq`, rows, projectID); err != nil {
		tb.Fatalf("insert channel target status fixture: %v", err)
	}
}

func BenchmarkChannelStatusMaterializedVsAggregateSummary(b *testing.B) {
	db := newAuthAllowlistBenchDB(b)
	seedAuthAllowlistFixture(b, db, authAllowlistBenchRows)
	seedChannelStatusTargetFixture(b, db, authAllowlistBenchProjectID, 25000)
	ctx := context.Background()
	repos := struct {
		slack    *SlackAuthRepo
		discord  *DiscordAuthRepo
		email    *EmailAuthRepo
		telegram *TelegramAuthRepo
		targets  *ChannelTargetRepo
	}{
		slack:    NewSlackAuthRepo(db),
		discord:  NewDiscordAuthRepo(db),
		email:    NewEmailAuthRepo(db),
		telegram: NewTelegramAuthRepo(db),
		targets:  NewChannelTargetRepo(db),
	}

	for _, tc := range []struct {
		name string
		run  func(context.Context) (int, error)
	}{
		{name: "materialized_lists", run: func(ctx context.Context) (int, error) {
			count, err := listSettingsAuthAllowlists(ctx, repos.slack, repos.discord, repos.email, repos.telegram)
			if err != nil {
				return 0, err
			}
			targets, err := repos.targets.ListByProject(ctx, authAllowlistBenchProjectID)
			if err != nil {
				return 0, err
			}
			return count + len(targets), nil
		}},
		{name: "aggregate_summary", run: func(ctx context.Context) (int, error) {
			slack, err := repos.slack.CountByProject(ctx, authAllowlistBenchProjectID)
			if err != nil {
				return 0, err
			}
			discord, err := repos.discord.CountByProject(ctx, authAllowlistBenchProjectID)
			if err != nil {
				return 0, err
			}
			email, err := repos.email.CountByProject(ctx, authAllowlistBenchProjectID)
			if err != nil {
				return 0, err
			}
			telegram, err := repos.telegram.CountByProject(ctx, authAllowlistBenchProjectID)
			if err != nil {
				return 0, err
			}
			targetSummary, err := repos.targets.SummarizeByProject(ctx, authAllowlistBenchProjectID)
			if err != nil {
				return 0, err
			}
			return slack + discord + email + telegram + targetSummary.Total, nil
		}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			warm, err := tc.run(ctx)
			if err != nil {
				b.Fatalf("warm channel status summary: %v", err)
			}
			if warm != authAllowlistBenchRows*4+25000 {
				b.Fatalf("warm channel status rows = %d, want %d", warm, authAllowlistBenchRows*4+25000)
			}
			durations := make([]time.Duration, 0, b.N)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				start := time.Now()
				got, err := tc.run(ctx)
				elapsed := time.Since(start)
				if err != nil {
					b.Fatalf("channel status summary: %v", err)
				}
				if got != authAllowlistBenchRows*4+25000 {
					b.Fatalf("channel status rows = %d, want %d", got, authAllowlistBenchRows*4+25000)
				}
				durations = append(durations, elapsed)
			}
			b.StopTimer()
			if len(durations) > 0 {
				sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
				median := durations[len(durations)/2]
				b.ReportMetric(float64(median.Nanoseconds())/1e6, "p50_ms")
			}
		})
	}
}

func TestChannelAuthAllowlistIdentityLookupPlansRemainIndexed(t *testing.T) {
	db := newAuthAllowlistBenchDB(t)
	seedAuthAllowlistFixture(t, db, 1500)
	setAuthAllowlistListIndexes(t, db, true)

	lookups := []struct {
		label string
		table string
		query string
		args  []any
	}{
		{
			label: "slack",
			table: "slack_authorized_users",
			query: `SELECT COUNT(*) FROM slack_authorized_users WHERE slack_user_id = ?`,
			args:  []any{"U0000001"},
		},
		{
			label: "discord",
			table: "discord_authorized_users",
			query: `SELECT COUNT(*) FROM discord_authorized_users WHERE discord_user_id = ?`,
			args:  []any{"900000000000000001"},
		},
		{
			label: "email",
			table: "email_authorized_senders",
			query: `SELECT COUNT(*) FROM email_authorized_senders WHERE lower(email_address) = lower(?)`,
			args:  []any{"USER0000001@EXAMPLE.COM"},
		},
		{
			label: "telegram",
			table: "telegram_authorized_users",
			query: `SELECT COUNT(*) FROM telegram_authorized_users
				WHERE telegram_user_id = ? OR (telegram_user_id = 0 AND telegram_username != '' AND LOWER(telegram_username) = LOWER(?))`,
			args: []any{int64(1), "tg0000001"},
		},
	}

	for _, lookup := range lookups {
		plan := authAllowlistExplain(t, db, lookup.query, lookup.args...)
		if strings.Contains(plan, authAllowlistListIndexes[lookup.label]) {
			t.Fatalf("%s lookup plan = %s, should not use list-order index", lookup.label, plan)
		}
		if strings.Contains(plan, "SCAN "+lookup.table) {
			t.Fatalf("%s lookup plan = %s, want existing identity index search", lookup.label, plan)
		}
	}
}

func assertAuthAllowlistRepositoryResults(t *testing.T, db *sql.DB, wantRows int) {
	t.Helper()
	ctx := context.Background()

	slackUsers, err := NewSlackAuthRepo(db).ListByProject(ctx, authAllowlistBenchProjectID)
	if err != nil {
		t.Fatalf("list slack users: %v", err)
	}
	assertOrderedAuthRows(t, "slack", slackUsers, wantRows, func(u models.SlackAuthorizedUser) (time.Time, string, string, string) {
		return u.AddedAt, u.ProjectID, u.SlackUserID, u.AddedBy
	})

	discordUsers, err := NewDiscordAuthRepo(db).ListByProject(ctx, authAllowlistBenchProjectID)
	if err != nil {
		t.Fatalf("list discord users: %v", err)
	}
	assertOrderedAuthRows(t, "discord", discordUsers, wantRows, func(u models.DiscordAuthorizedUser) (time.Time, string, string, string) {
		return u.AddedAt, u.ProjectID, u.DiscordUserID, u.AddedBy
	})

	emailSenders, err := NewEmailAuthRepo(db).ListByProject(ctx, authAllowlistBenchProjectID)
	if err != nil {
		t.Fatalf("list email senders: %v", err)
	}
	assertOrderedAuthRows(t, "email", emailSenders, wantRows, func(s models.EmailAuthorizedSender) (time.Time, string, string, string) {
		return s.AddedAt, s.ProjectID, s.EmailAddress, s.AddedBy
	})

	telegramUsers, err := NewTelegramAuthRepo(db).ListByProject(ctx, authAllowlistBenchProjectID)
	if err != nil {
		t.Fatalf("list telegram users: %v", err)
	}
	assertOrderedAuthRows(t, "telegram", telegramUsers, wantRows, func(u models.TelegramAuthorizedUser) (time.Time, string, string, string) {
		return u.AddedAt, u.ProjectID, u.TelegramUsername, u.AddedBy
	})
}

func assertOrderedAuthRows[T any](t *testing.T, label string, rows []T, wantRows int, fields func(T) (time.Time, string, string, string)) {
	t.Helper()
	if len(rows) != wantRows {
		t.Fatalf("%s rows = %d, want %d", label, len(rows), wantRows)
	}
	for i, row := range rows {
		addedAt, projectID, identity, addedBy := fields(row)
		if projectID != authAllowlistBenchProjectID {
			t.Fatalf("%s row %d project_id = %q, want %q", label, i, projectID, authAllowlistBenchProjectID)
		}
		if identity == "" {
			t.Fatalf("%s row %d identity is empty", label, i)
		}
		if addedBy != "bench" {
			t.Fatalf("%s row %d added_by = %q, want bench", label, i, addedBy)
		}
		if i > 0 {
			prevAddedAt, _, _, _ := fields(rows[i-1])
			if addedAt.Before(prevAddedAt) {
				t.Fatalf("%s row %d is out of added_at ASC order", label, i)
			}
		}
	}
}

// BenchmarkChannelAuthAllowlistSettingsPathListQueries measures the Settings
// path's four sequential allowlist repository calls against randomized added_at
// rows. Baseline drops the migration-152 list indexes; candidate recreates the
// covering list-order indexes. Run with:
//
//	go test ./internal/repository -run '^$' -bench BenchmarkChannelAuthAllowlistSettingsPathListQueries -benchmem -count=10
//
// and compare with benchstat. The p50_ms metric reports the per-iteration median
// latency for the combined Slack, Discord, email, and Telegram list refresh path.
func BenchmarkChannelAuthAllowlistSettingsPathListQueries(b *testing.B) {
	db := newAuthAllowlistBenchDB(b)
	seedAuthAllowlistFixture(b, db, authAllowlistBenchRows)
	ctx := context.Background()
	repos := struct {
		slack    *SlackAuthRepo
		discord  *DiscordAuthRepo
		email    *EmailAuthRepo
		telegram *TelegramAuthRepo
	}{
		slack:    NewSlackAuthRepo(db),
		discord:  NewDiscordAuthRepo(db),
		email:    NewEmailAuthRepo(db),
		telegram: NewTelegramAuthRepo(db),
	}

	for _, tc := range []struct {
		name      string
		candidate bool
	}{
		{"baseline", false},
		{"candidate", true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			setAuthAllowlistListIndexes(b, db, tc.candidate)

			warm, err := listSettingsAuthAllowlists(ctx, repos.slack, repos.discord, repos.email, repos.telegram)
			if err != nil {
				b.Fatalf("warm settings auth allowlist query: %v", err)
			}
			if warm != authAllowlistBenchRows*4 {
				b.Fatalf("warm settings auth allowlist rows = %d, want %d", warm, authAllowlistBenchRows*4)
			}

			durations := make([]time.Duration, 0, b.N)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				start := time.Now()
				got, err := listSettingsAuthAllowlists(ctx, repos.slack, repos.discord, repos.email, repos.telegram)
				elapsed := time.Since(start)
				if err != nil {
					b.Fatalf("settings auth allowlist query: %v", err)
				}
				if got != authAllowlistBenchRows*4 {
					b.Fatalf("settings auth allowlist rows = %d, want %d", got, authAllowlistBenchRows*4)
				}
				durations = append(durations, elapsed)
			}
			b.StopTimer()

			if len(durations) > 0 {
				sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
				median := durations[len(durations)/2]
				b.ReportMetric(float64(median.Nanoseconds())/1e6, "p50_ms")
			}
		})
	}
}

func listSettingsAuthAllowlists(ctx context.Context, slack *SlackAuthRepo, discord *DiscordAuthRepo, email *EmailAuthRepo, telegram *TelegramAuthRepo) (int, error) {
	slackUsers, err := slack.ListByProject(ctx, authAllowlistBenchProjectID)
	if err != nil {
		return 0, err
	}
	discordUsers, err := discord.ListByProject(ctx, authAllowlistBenchProjectID)
	if err != nil {
		return 0, err
	}
	emailSenders, err := email.ListByProject(ctx, authAllowlistBenchProjectID)
	if err != nil {
		return 0, err
	}
	telegramUsers, err := telegram.ListByProject(ctx, authAllowlistBenchProjectID)
	if err != nil {
		return 0, err
	}
	return len(slackUsers) + len(discordUsers) + len(emailSenders) + len(telegramUsers), nil
}
