package server

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/database"
)

func TestStart_BootstrapAndShutdown(t *testing.T) {
	// Smoke-test: start the full server with an in-memory-style temp DB and
	// an ephemeral port, hit a core route, then shut down gracefully.
	tmpDir := t.TempDir()
	appDataDir := filepath.Join(tmpDir, "appdata")
	cfg := &config.Config{
		Mode:            config.ModeDesktop,
		Port:            "0", // ephemeral port
		DatabasePath:    tmpDir + "/test.db",
		ProjectRepoRoot: tmpDir + "/repos",
		AppDataDir:      appDataDir,
		Environment:     "test",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inst, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer inst.Shutdown()

	if inst.BoundAddr == "" {
		t.Fatal("expected non-empty BoundAddr")
	}
	if inst.BaseURL == "" {
		t.Fatal("expected non-empty BaseURL")
	}
	if _, err := os.Stat(filepath.Join(appDataDir, "agents", "AGENTS.md")); err != nil {
		t.Fatalf("expected built-in AGENTS.md at app data agents root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appDataDir, "agents", "skill_curator", "SKILLS.md")); err != nil {
		t.Fatalf("expected built-in skill_curator SKILLS.md at app data agents root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appDataDir, "agents", "agents", "AGENTS.md")); err == nil {
		t.Fatal("built-in index was written under appdata/agents/agents; expected appdata/agents")
	}

	// Hit a core route to verify the server is serving.
	client := &http.Client{Timeout: 5 * time.Second}

	// The root should redirect to /chat
	resp, err := client.Get(inst.BaseURL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	resp.Body.Close()
	// Accept redirect (302) or the final page (200).
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
		t.Fatalf("GET / returned %d, expected 200 or 302", resp.StatusCode)
	}

	// Swagger spec should be reachable.
	resp, err = client.Get(inst.BaseURL + "/swagger/doc.json")
	if err != nil {
		t.Fatalf("GET /swagger/doc.json failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /swagger/doc.json returned %d, expected 200", resp.StatusCode)
	}

	// Graceful shutdown.
	inst.Shutdown()
}

func TestStart_SeedsBuiltInSystemAgentsAndMaintenanceSchedulesOnFreshDB(t *testing.T) {
	appDataDir := filepath.Join(t.TempDir(), "fresh-appdata")
	cfg := &config.Config{
		Mode:        config.ModeServer,
		Port:        "0",
		AppDataDir:  appDataDir,
		Environment: "test",
	}

	ctx, cancel := context.WithCancel(context.Background())
	inst, err := Start(ctx, cfg)
	if err != nil {
		cancel()
		t.Fatalf("Start() fresh failed: %v", err)
	}
	inst.Shutdown()
	cancel()

	assertFreshSystemSeedState(t, cfg.DatabasePath)

	restartCtx, restartCancel := context.WithCancel(context.Background())
	restarted, err := Start(restartCtx, cfg)
	if err != nil {
		restartCancel()
		t.Fatalf("Start() restart failed: %v", err)
	}
	restarted.Shutdown()
	restartCancel()

	assertFreshSystemSeedState(t, cfg.DatabasePath)
}

func assertFreshSystemSeedState(t *testing.T, dbPath string) {
	t.Helper()
	db, err := database.New(dbPath)
	if err != nil {
		t.Fatalf("open seeded db: %v", err)
	}
	defer db.Close()

	assertSingleProtectedSystemAgent(t, db, "memory_curator")
	assertSingleProtectedSystemAgent(t, db, "skill_curator")
	assertSingleProtectedSystemAgent(t, db, "goal")
	assertSingleScheduledSystemTask(t, db, "System: Memory Consolidation", "memory_curator")
	assertSingleScheduledSystemTask(t, db, "System: Skill Library Maintenance", "skill_curator")

	var maxWorkers int
	if err := db.QueryRow("SELECT max_workers FROM worker_settings WHERE id='singleton'").Scan(&maxWorkers); err != nil {
		t.Fatalf("read global worker limit after startup: %v", err)
	}
	if maxWorkers != 0 {
		t.Fatalf("global worker limit after startup = %d, want unlimited (0)", maxWorkers)
	}
}

func assertSingleProtectedSystemAgent(t *testing.T, db *sql.DB, systemKind string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM agents
		WHERE system_kind = ?
		  AND key = ?
		  AND generated_status = 'protected'
		  AND created_by = 'system'
		  AND enabled = 1
		  AND selectable_as_primary = 0
	`, systemKind, systemKind).Scan(&count); err != nil {
		t.Fatalf("count protected system agent %s: %v", systemKind, err)
	}
	if count != 1 {
		t.Fatalf("expected one protected %s agent, got %d", systemKind, count)
	}
}

func assertSingleScheduledSystemTask(t *testing.T, db *sql.DB, title, systemKind string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM tasks t
		JOIN agents a ON a.id = t.agent_definition_id
		JOIN schedules s ON s.task_id = t.id
		WHERE t.project_id = 'default'
		  AND t.title = ?
		  AND t.category = 'scheduled'
		  AND a.system_kind = ?
		  AND s.repeat_type = 'daily'
		  AND s.repeat_interval = 1
		  AND s.enabled = 1
	`, title, systemKind).Scan(&count); err != nil {
		t.Fatalf("count scheduled system task %s: %v", title, err)
	}
	if count != 1 {
		t.Fatalf("expected one scheduled system task %q for %s, got %d", title, systemKind, count)
	}
}

func TestStart_NormalizesAppStorageDefaults(t *testing.T) {
	for _, mode := range []config.RuntimeMode{config.ModeServer, config.ModeDesktop} {
		t.Run(string(mode), func(t *testing.T) {
			appDataDir := filepath.Join(t.TempDir(), "appdata")
			cfg := &config.Config{
				Mode:        mode,
				Port:        "0",
				AppDataDir:  appDataDir,
				Environment: "test",
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			inst, err := Start(ctx, cfg)
			if err != nil {
				t.Fatalf("Start() failed: %v", err)
			}
			defer inst.Shutdown()

			if cfg.AppDataDir == "" {
				t.Fatalf("expected Start to normalize %s AppDataDir", mode)
			}
			if cfg.DatabasePath != filepath.Join(cfg.AppDataDir, "openvibely.db") {
				t.Fatalf("DatabasePath=%q want app-data DB under %q", cfg.DatabasePath, cfg.AppDataDir)
			}
			if cfg.ProjectRepoRoot != filepath.Join(cfg.AppDataDir, "repos") {
				t.Fatalf("ProjectRepoRoot=%q want app-data repos under %q", cfg.ProjectRepoRoot, cfg.AppDataDir)
			}
			if _, err := os.Stat(filepath.Join(cfg.AppDataDir, "agents", "AGENTS.md")); err != nil {
				t.Fatalf("expected built-in agents index under normalized app data root: %v", err)
			}
		})
	}
}

func TestMigrateLegacyStorageMovesDefaultDatabaseSidecarsAndRepos(t *testing.T) {
	t.Setenv("DATABASE_PATH", "")
	t.Setenv("PROJECT_REPO_ROOT", "")
	t.Setenv("OPENVIBELY_DISABLE_LEGACY_STORAGE_MIGRATION", "")
	tmpDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	for _, name := range []string{"openvibely.db", "openvibely.db-wal", "openvibely.db-shm", "openvibely.db-journal"} {
		if err := os.WriteFile(name, []byte(name), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.MkdirAll(filepath.Join("repos", "example"), 0o755); err != nil {
		t.Fatalf("mkdir repos: %v", err)
	}
	if err := os.MkdirAll(filepath.Join("uploads", "tasks", "task-1"), 0o755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	if err := os.WriteFile(filepath.Join("uploads", "tasks", "task-1", "file.txt"), []byte("upload"), 0o644); err != nil {
		t.Fatalf("write upload: %v", err)
	}

	appDataDir := filepath.Join(tmpDir, "appdata")
	cfg := &config.Config{
		AppDataDir:      appDataDir,
		DatabasePath:    filepath.Join(appDataDir, "openvibely.db"),
		ProjectRepoRoot: filepath.Join(appDataDir, "repos"),
	}
	if err := migrateLegacyStorage(cfg); err != nil {
		t.Fatalf("migrateLegacyStorage: %v", err)
	}

	for _, name := range []string{"openvibely.db", "openvibely.db-wal", "openvibely.db-shm", "openvibely.db-journal"} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("expected legacy %s to be moved, stat err=%v", name, err)
		}
		if data, err := os.ReadFile(filepath.Join(appDataDir, name)); err != nil || string(data) != name {
			t.Fatalf("expected migrated %s content, data=%q err=%v", name, string(data), err)
		}
	}
	if _, err := os.Stat(filepath.Join("repos", "example")); !os.IsNotExist(err) {
		t.Fatalf("expected legacy repos directory to be moved, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(appDataDir, "repos", "example")); err != nil {
		t.Fatalf("expected migrated repo directory: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(appDataDir, "uploads", "tasks", "task-1", "file.txt")); err != nil || string(data) != "upload" {
		t.Fatalf("expected migrated upload file, data=%q err=%v", string(data), err)
	}
}

func TestMigrateLegacyStoragePreservesExistingTargetDatabaseAndRepos(t *testing.T) {
	t.Setenv("DATABASE_PATH", "")
	t.Setenv("PROJECT_REPO_ROOT", "")
	t.Setenv("OPENVIBELY_DISABLE_LEGACY_STORAGE_MIGRATION", "")
	tmpDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	for _, name := range []string{"openvibely.db", "openvibely.db-wal", "openvibely.db-shm", "openvibely.db-journal"} {
		if err := os.WriteFile(name, []byte("legacy-"+name), 0o644); err != nil {
			t.Fatalf("write legacy %s: %v", name, err)
		}
	}
	if err := os.MkdirAll(filepath.Join("repos", "real-project"), 0o755); err != nil {
		t.Fatalf("mkdir legacy repos: %v", err)
	}
	if err := os.WriteFile(filepath.Join("repos", "real-project", "README.md"), []byte("legacy repo"), 0o644); err != nil {
		t.Fatalf("write legacy repo file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join("uploads", "chat", "legacy-exec"), 0o755); err != nil {
		t.Fatalf("mkdir legacy uploads: %v", err)
	}
	if err := os.WriteFile(filepath.Join("uploads", "chat", "legacy-exec", "image.png"), []byte("legacy upload"), 0o644); err != nil {
		t.Fatalf("write legacy upload file: %v", err)
	}

	appDataDir := filepath.Join(tmpDir, "appdata")
	if err := os.MkdirAll(filepath.Join(appDataDir, "repos", "empty-project"), 0o755); err != nil {
		t.Fatalf("mkdir target repos: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(appDataDir, "uploads", "chat", "target-exec"), 0o755); err != nil {
		t.Fatalf("mkdir target uploads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appDataDir, "uploads", "chat", "target-exec", "image.png"), []byte("target upload"), 0o644); err != nil {
		t.Fatalf("write target upload file: %v", err)
	}
	freshDB := "SQLite format 3\x00fresh-db"
	for _, name := range []string{"openvibely.db", "openvibely.db-wal", "openvibely.db-shm", "openvibely.db-journal"} {
		content := "fresh-" + name
		if name == "openvibely.db" {
			content = freshDB
		}
		if err := os.WriteFile(filepath.Join(appDataDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write fresh target %s: %v", name, err)
		}
	}
	if err := os.Remove("openvibely.db-wal"); err != nil {
		t.Fatalf("remove legacy wal to verify stale target sidecar backup: %v", err)
	}

	cfg := &config.Config{
		AppDataDir:      appDataDir,
		DatabasePath:    filepath.Join(appDataDir, "openvibely.db"),
		ProjectRepoRoot: filepath.Join(appDataDir, "repos"),
	}
	if err := migrateLegacyStorage(cfg); err != nil {
		t.Fatalf("migrateLegacyStorage: %v", err)
	}

	for _, name := range []string{"openvibely.db", "openvibely.db-wal", "openvibely.db-shm", "openvibely.db-journal"} {
		want := "fresh-" + name
		if name == "openvibely.db" {
			want = freshDB
		}
		if data, err := os.ReadFile(filepath.Join(appDataDir, name)); err != nil || string(data) != want {
			t.Fatalf("expected fresh target %s content to be preserved, data=%q err=%v", name, string(data), err)
		}
	}
	for _, name := range []string{"openvibely.db", "openvibely.db-shm", "openvibely.db-journal"} {
		if data, err := os.ReadFile(name); err != nil || string(data) != "legacy-"+name {
			t.Fatalf("expected legacy %s to be left in place, data=%q err=%v", name, string(data), err)
		}
	}
	if _, err := os.Stat("openvibely.db-wal"); !os.IsNotExist(err) {
		t.Fatalf("expected missing legacy wal to remain absent, stat err=%v", err)
	}
	for _, name := range []string{"openvibely.db", "openvibely.db-wal", "openvibely.db-shm", "openvibely.db-journal"} {
		if _, err := os.Stat(filepath.Join(appDataDir, name+".pre-appdata-migration-backup")); !os.IsNotExist(err) {
			t.Fatalf("expected no database backup for preserved target %s, stat err=%v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(appDataDir, "repos", "empty-project")); err != nil {
		t.Fatalf("expected existing target repos to be preserved: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join("repos", "real-project", "README.md")); err != nil || string(data) != "legacy repo" {
		t.Fatalf("expected legacy repo to be left in place, data=%q err=%v", string(data), err)
	}
	if _, err := os.Stat(filepath.Join(appDataDir, "repos.pre-appdata-migration-backup")); !os.IsNotExist(err) {
		t.Fatalf("expected no repos backup for preserved target, stat err=%v", err)
	}
	if data, err := os.ReadFile(filepath.Join(appDataDir, "uploads", "chat", "target-exec", "image.png")); err != nil || string(data) != "target upload" {
		t.Fatalf("expected existing target upload to be preserved, data=%q err=%v", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join("uploads", "chat", "legacy-exec", "image.png")); err != nil || string(data) != "legacy upload" {
		t.Fatalf("expected legacy upload to be left in place, data=%q err=%v", string(data), err)
	}
	if _, err := os.Stat(filepath.Join(appDataDir, "uploads.pre-appdata-migration-backup")); !os.IsNotExist(err) {
		t.Fatalf("expected no uploads backup for preserved target, stat err=%v", err)
	}
}

func TestMigrateLegacyStorageMigratesOverEmptyOrInvalidTargets(t *testing.T) {
	t.Setenv("DATABASE_PATH", "")
	t.Setenv("PROJECT_REPO_ROOT", "")
	t.Setenv("OPENVIBELY_DISABLE_LEGACY_STORAGE_MIGRATION", "")
	tmpDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	if err := os.WriteFile("openvibely.db", []byte("legacy-db"), 0o644); err != nil {
		t.Fatalf("write legacy db: %v", err)
	}
	if err := os.MkdirAll(filepath.Join("repos", "real-project"), 0o755); err != nil {
		t.Fatalf("mkdir legacy repos: %v", err)
	}
	if err := os.WriteFile(filepath.Join("repos", "real-project", "README.md"), []byte("legacy repo"), 0o644); err != nil {
		t.Fatalf("write legacy repo file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join("uploads", "chat", "exec-1"), 0o755); err != nil {
		t.Fatalf("mkdir legacy uploads: %v", err)
	}
	if err := os.WriteFile(filepath.Join("uploads", "chat", "exec-1", "image.png"), []byte("legacy upload"), 0o644); err != nil {
		t.Fatalf("write legacy upload file: %v", err)
	}

	appDataDir := filepath.Join(tmpDir, "appdata")
	if err := os.MkdirAll(filepath.Join(appDataDir, "repos"), 0o755); err != nil {
		t.Fatalf("mkdir empty target repos: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(appDataDir, "uploads"), 0o755); err != nil {
		t.Fatalf("mkdir empty target uploads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appDataDir, "openvibely.db"), []byte("not a sqlite database"), 0o644); err != nil {
		t.Fatalf("write invalid target db: %v", err)
	}

	cfg := &config.Config{
		AppDataDir:      appDataDir,
		DatabasePath:    filepath.Join(appDataDir, "openvibely.db"),
		ProjectRepoRoot: filepath.Join(appDataDir, "repos"),
	}
	if err := migrateLegacyStorage(cfg); err != nil {
		t.Fatalf("migrateLegacyStorage: %v", err)
	}

	if data, err := os.ReadFile(filepath.Join(appDataDir, "openvibely.db")); err != nil || string(data) != "legacy-db" {
		t.Fatalf("expected legacy db migrated over empty target, data=%q err=%v", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(appDataDir, "repos", "real-project", "README.md")); err != nil || string(data) != "legacy repo" {
		t.Fatalf("expected legacy repo migrated over empty target, data=%q err=%v", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(appDataDir, "uploads", "chat", "exec-1", "image.png")); err != nil || string(data) != "legacy upload" {
		t.Fatalf("expected legacy upload migrated over empty target, data=%q err=%v", string(data), err)
	}
	if _, err := os.Stat(filepath.Join(appDataDir, "openvibely.db.pre-appdata-migration-backup")); err != nil {
		t.Fatalf("expected invalid target db backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appDataDir, "repos.pre-appdata-migration-backup")); err != nil {
		t.Fatalf("expected empty target repos backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appDataDir, "uploads.pre-appdata-migration-backup")); err != nil {
		t.Fatalf("expected empty target uploads backup: %v", err)
	}
}

func TestMigrateLegacyStorageMovesUploadsIntoAppData(t *testing.T) {
	t.Setenv("DATABASE_PATH", "")
	t.Setenv("PROJECT_REPO_ROOT", "")
	t.Setenv("OPENVIBELY_DISABLE_LEGACY_STORAGE_MIGRATION", "")
	tmpDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	if err := os.MkdirAll(filepath.Join("uploads", "chat", "exec-1"), 0o755); err != nil {
		t.Fatalf("mkdir uploads: %v", err)
	}
	if err := os.WriteFile(filepath.Join("uploads", "chat", "exec-1", "image.png"), []byte("image"), 0o644); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	appDataDir := filepath.Join(tmpDir, "appdata")

	cfg := &config.Config{
		AppDataDir:      appDataDir,
		DatabasePath:    filepath.Join(appDataDir, "openvibely.db"),
		ProjectRepoRoot: filepath.Join(appDataDir, "repos"),
	}
	if err := migrateLegacyStorage(cfg); err != nil {
		t.Fatalf("migrateLegacyStorage: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(appDataDir, "uploads", "chat", "exec-1", "image.png")); err != nil || string(data) != "image" {
		t.Fatalf("expected migrated upload, data=%q err=%v", string(data), err)
	}
	if _, err := os.Stat(filepath.Join(appDataDir, "uploads.pre-appdata-migration-backup")); !os.IsNotExist(err) {
		t.Fatalf("expected no target uploads backup when target was absent, stat err=%v", err)
	}
}

func TestMigrateLegacyStorageRespectsExplicitStorageEnv(t *testing.T) {
	tmpDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	t.Setenv("DATABASE_PATH", filepath.Join(tmpDir, "custom.db"))
	t.Setenv("PROJECT_REPO_ROOT", filepath.Join(tmpDir, "custom-repos"))

	if err := os.WriteFile("openvibely.db", []byte("legacy"), 0o644); err != nil {
		t.Fatalf("write legacy db: %v", err)
	}
	if err := os.MkdirAll(filepath.Join("repos", "example"), 0o755); err != nil {
		t.Fatalf("mkdir legacy repos: %v", err)
	}
	cfg := &config.Config{
		DatabasePath:    filepath.Join(tmpDir, "appdata", "openvibely.db"),
		ProjectRepoRoot: filepath.Join(tmpDir, "appdata", "repos"),
	}
	if err := migrateLegacyStorage(cfg); err != nil {
		t.Fatalf("migrateLegacyStorage: %v", err)
	}
	if _, err := os.Stat("openvibely.db"); err != nil {
		t.Fatalf("expected explicit env to leave legacy db alone: %v", err)
	}
	if _, err := os.Stat(filepath.Join("repos", "example")); err != nil {
		t.Fatalf("expected explicit env to leave legacy repos alone: %v", err)
	}
}

func TestMigrateLegacyStorageRespectsExplicitAppDataDir(t *testing.T) {
	tmpDir := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	t.Setenv("DATABASE_PATH", "")
	t.Setenv("PROJECT_REPO_ROOT", "")
	t.Setenv("OPENVIBELY_APP_DATA_DIR", filepath.Join(tmpDir, "custom-appdata"))

	if err := os.WriteFile("openvibely.db", []byte("legacy"), 0o644); err != nil {
		t.Fatalf("write legacy db: %v", err)
	}
	if err := os.MkdirAll(filepath.Join("repos", "example"), 0o755); err != nil {
		t.Fatalf("mkdir legacy repos: %v", err)
	}
	cfg := &config.Config{
		DatabasePath:    filepath.Join(tmpDir, "custom-appdata", "openvibely.db"),
		ProjectRepoRoot: filepath.Join(tmpDir, "custom-appdata", "repos"),
	}
	if err := migrateLegacyStorage(cfg); err != nil {
		t.Fatalf("migrateLegacyStorage: %v", err)
	}
	if _, err := os.Stat("openvibely.db"); err != nil {
		t.Fatalf("expected explicit app data dir to leave legacy db alone: %v", err)
	}
	if _, err := os.Stat(filepath.Join("repos", "example")); err != nil {
		t.Fatalf("expected explicit app data dir to leave legacy repos alone: %v", err)
	}
}

func TestStart_ServerModeDefaults(t *testing.T) {
	// Verify existing server mode still works with explicit port.
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Mode:            config.ModeServer,
		Port:            "0",
		DatabasePath:    tmpDir + "/test.db",
		ProjectRepoRoot: tmpDir + "/repos",
		Environment:     "test",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inst, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer inst.Shutdown()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(inst.BaseURL + "/models")
	if err != nil {
		t.Fatalf("GET /models failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /models returned %d, expected 200", resp.StatusCode)
	}
}
