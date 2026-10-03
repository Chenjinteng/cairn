package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// columnInfo is the subset of PRAGMA table_info(...) this suite asserts on:
// declared type, nullability and default expression.
type columnInfo struct {
	typ     string
	notNull bool
	dflt    string
	hasDflt bool
}

// tableColumns reads PRAGMA table_info(table) into a name → columnInfo map.
// PRAGMA table_info returns (cid, name, type, notnull, dflt_value, pk).
func tableColumns(t *testing.T, conn *sql.DB, table string) map[string]columnInfo {
	t.Helper()
	rows, err := conn.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()

	out := map[string]columnInfo{}
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("table_info(%s): scan: %v", table, err)
		}
		out[name] = columnInfo{
			typ:     typ,
			notNull: notNull == 1,
			dflt:    dflt.String,
			hasDflt: dflt.Valid,
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info(%s): rows: %v", table, err)
	}
	return out
}

// tempDBPath returns a not-yet-created DB path inside the test's temp dir.
func tempDBPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

// fixtureV6DB builds a database that looks exactly like one opened by
// v0.6.7: migrations 1..6 applied, user_version = 6, and one legacy sync
// task carrying inline credentials (no remote_credential_id column yet).
func fixtureV6DB(t *testing.T, path string) {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("fixture: open: %v", err)
	}
	defer conn.Close()

	for v := 1; v <= 6; v++ {
		body, ok := migrations[v]
		if !ok {
			t.Fatalf("fixture: migrations[%d] missing", v)
		}
		if _, err := conn.Exec(body); err != nil {
			t.Fatalf("fixture: migration %d: %v", v, err)
		}
	}
	if _, err := conn.Exec("PRAGMA user_version = 6"); err != nil {
		t.Fatalf("fixture: set user_version: %v", err)
	}
	_, err = conn.Exec(`INSERT INTO sync_tasks
		(name, direction, remote_url, remote_username, remote_password, include, enabled, created_at, updated_at)
		VALUES ('legacy', 'pull', 'http://192.0.2.1:10001', 'alice', 's3cret', '', 1, 100, 200)`)
	if err != nil {
		t.Fatalf("fixture: insert legacy task: %v", err)
	}
}

func TestMigrateFreshDatabase(t *testing.T) {
	path := tempDBPath(t, "fresh.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	var v int
	if err := d.conn.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	if v != SCHEMA_VERSION {
		t.Fatalf("user_version = %d, want %d", v, SCHEMA_VERSION)
	}

	// sync_tasks: the v6 column set plus SYNC-3's remote_credential_id
	// plus v0.7.21's tags_filter plus v0.7.22's long_timeout_repos.
	wantTasks := []string{
		"id", "name", "direction", "remote_url", "remote_username",
		"remote_password", "include", "enabled", "created_at", "updated_at",
		"remote_credential_id", "tags_filter", "long_timeout_repos",
	}
	gotTasks := tableColumns(t, d.conn, "sync_tasks")
	if len(gotTasks) != len(wantTasks) {
		t.Fatalf("sync_tasks columns = %d, want %d (%v)", len(gotTasks), len(wantTasks), gotTasks)
	}
	for _, name := range wantTasks {
		if _, ok := gotTasks[name]; !ok {
			t.Errorf("sync_tasks missing column %q", name)
		}
	}
	ref, ok := gotTasks["remote_credential_id"]
	if !ok {
		t.Fatal("sync_tasks.remote_credential_id missing (SYNC-3)")
	}
	if !strings.EqualFold(ref.typ, "TEXT") {
		t.Errorf("remote_credential_id type = %q, want TEXT", ref.typ)
	}
	if !ref.notNull {
		t.Error("remote_credential_id must be NOT NULL")
	}
	if !ref.hasDflt || strings.Trim(ref.dflt, "'") != "" {
		t.Errorf("remote_credential_id default = %q (hasDefault=%v), want empty-string default", ref.dflt, ref.hasDflt)
	}
	// v0.7.21: tags_filter — same TEXT NOT NULL DEFAULT '' contract as
	// remote_credential_id; the engine parses it at run time, empty
	// string preserves the original catalog-path behavior so pre-0.7.21
	// tasks keep working unchanged.
	tf, ok := gotTasks["tags_filter"]
	if !ok {
		t.Fatal("sync_tasks.tags_filter missing (v0.7.21)")
	}
	if !strings.EqualFold(tf.typ, "TEXT") {
		t.Errorf("tags_filter type = %q, want TEXT", tf.typ)
	}
	if !tf.notNull {
		t.Error("tags_filter must be NOT NULL")
	}
	if !tf.hasDflt || strings.Trim(tf.dflt, "'") != "" {
		t.Errorf("tags_filter default = %q (hasDefault=%v), want empty-string default", tf.dflt, tf.hasDflt)
	}
	// v0.7.22: long_timeout_repos — same contract; the engine picks
	// 30min timeout for matching repos, empty string preserves the
	// 5min default so pre-0.7.22 tasks keep their old behavior.
	lt, ok := gotTasks["long_timeout_repos"]
	if !ok {
		t.Fatal("sync_tasks.long_timeout_repos missing (v0.7.22)")
	}
	if !strings.EqualFold(lt.typ, "TEXT") {
		t.Errorf("long_timeout_repos type = %q, want TEXT", lt.typ)
	}
	if !lt.notNull {
		t.Error("long_timeout_repos must be NOT NULL")
	}
	if !lt.hasDflt || strings.Trim(lt.dflt, "'") != "" {
		t.Errorf("long_timeout_repos default = %q (hasDefault=%v), want empty-string default", lt.dflt, lt.hasDflt)
	}

	wantRuns := []string{
		"id", "task_id", "started_at", "finished_at", "status",
		"repos_total", "repos_synced", "repos_failed", "error",
		"current_repo", "current_tag",
	}
	gotRuns := tableColumns(t, d.conn, "sync_runs")
	if len(gotRuns) != len(wantRuns) {
		t.Fatalf("sync_runs columns = %d, want %d (%v)", len(gotRuns), len(wantRuns), gotRuns)
	}
	for _, name := range wantRuns {
		if _, ok := gotRuns[name]; !ok {
			t.Errorf("sync_runs missing column %q", name)
		}
	}

	// sync_runs.current_repo / current_tag (v0.6.9): same contract as
	// remote_credential_id above — TEXT NOT NULL DEFAULT ''. The engine
	// stamps the current (repo, tag) on every iteration step.
	for _, name := range []string{"current_repo", "current_tag"} {
		col, ok := gotRuns[name]
		if !ok {
			t.Errorf("sync_runs.%s missing (v0.6.9)", name)
			continue
		}
		if !strings.EqualFold(col.typ, "TEXT") {
			t.Errorf("sync_runs.%s type = %q, want TEXT", name, col.typ)
		}
		if !col.notNull {
			t.Errorf("sync_runs.%s must be NOT NULL", name)
		}
		if !col.hasDflt || strings.Trim(col.dflt, "'") != "" {
			t.Errorf("sync_runs.%s default = %q (hasDefault=%v), want empty-string default", name, col.dflt, col.hasDflt)
		}
	}
}

// fixtureV7DB builds a database that looks exactly like one opened by
// v0.6.8: migrations 1..7 applied, user_version = 7, one legacy sync
// task with inline credentials, and one sync_runs row at status
// 'running' (representing a zombie run that survived from 0.6.8 — the
// v0.6.9 migration must preserve it without rewriting it).
func fixtureV7DB(t *testing.T, path string) {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("fixture: open: %v", err)
	}
	defer conn.Close()

	for v := 1; v <= 7; v++ {
		body, ok := migrations[v]
		if !ok {
			t.Fatalf("fixture: migrations[%d] missing", v)
		}
		if _, err := conn.Exec(body); err != nil {
			t.Fatalf("fixture: migration %d: %v", v, err)
		}
	}
	if _, err := conn.Exec("PRAGMA user_version = 7"); err != nil {
		t.Fatalf("fixture: set user_version: %v", err)
	}
	res, err := conn.Exec(`INSERT INTO sync_tasks
		(name, direction, remote_url, remote_username, remote_password, include, enabled, created_at, updated_at)
		VALUES ('legacy', 'pull', 'http://192.0.2.1:10001', 'alice', 's3cret', '', 1, 100, 200)`)
	if err != nil {
		t.Fatalf("fixture: insert legacy task: %v", err)
	}
	taskID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("fixture: task id: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO sync_runs
		(task_id, started_at, status, repos_total, repos_synced, repos_failed, error)
		VALUES (?, 1000, 'running', 5, 2, 0, '')`, taskID); err != nil {
		t.Fatalf("fixture: insert legacy run: %v", err)
	}
}

func TestMigrateV6ToV7PreservesLegacyInlineTask(t *testing.T) {
	path := tempDBPath(t, "v6.db")
	fixtureV6DB(t, path)

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open after v6 fixture: %v", err)
	}

	var v int
	if err := d.conn.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		d.Close()
		t.Fatalf("user_version: %v", err)
	}
	if v != 13 {
		d.Close()
		t.Fatalf("user_version after upgrade = %d, want 13 (v6 → v7+v8+v9+v10+v11+v12+v13 migrations run in one Open)", v)
	}

	var (
		url, user, pass, ref string
	)
	err = d.conn.QueryRow(
		`SELECT remote_url, remote_username, remote_password, remote_credential_id
		   FROM sync_tasks WHERE name = 'legacy'`,
	).Scan(&url, &user, &pass, &ref)
	if err != nil {
		d.Close()
		t.Fatalf("select legacy task: %v", err)
	}
	if url != "http://192.0.2.1:10001" || user != "alice" || pass != "s3cret" {
		t.Errorf("legacy inline credentials mutated: url=%q user=%q pass=%q", url, user, pass)
	}
	if ref != "" {
		t.Errorf("legacy task remote_credential_id = %q, want empty", ref)
	}

	// Idempotency: a second Open must not re-run the ALTER (which would
	// fail with "duplicate column name" if the guard were broken).
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer d2.Close()

	if err := d2.conn.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version after reopen: %v", err)
	}
	if v != 13 {
		t.Fatalf("user_version after reopen = %d, want 13", v)
	}
	if _, ok := tableColumns(t, d2.conn, "sync_tasks")["remote_credential_id"]; !ok {
		t.Fatal("remote_credential_id missing after reopen")
	}
}

func TestMigrateV7ToV8PreservesLegacyRun(t *testing.T) {
	path := tempDBPath(t, "v7.db")
	fixtureV7DB(t, path)

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open after v7 fixture: %v", err)
	}

	var v int
	if err := d.conn.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		d.Close()
		t.Fatalf("user_version: %v", err)
	}
	if v != 13 {
		d.Close()
		t.Fatalf("user_version after upgrade = %d, want 13", v)
	}

	// New columns present + readable.
	for _, name := range []string{"current_repo", "current_tag"} {
		if _, ok := tableColumns(t, d.conn, "sync_runs")[name]; !ok {
			d.Close()
			t.Fatalf("sync_runs.%s missing after v7→v8 migration", name)
		}
	}

	// Legacy run row preserved with status='running', counters intact, and
	// current_repo / current_tag default to '' (so the startup sweep can
	// later flip this zombie to 'failed' without surprise).
	var (
		status               string
		reposTotal, synced   int
		currentRepo, currentTag string
	)
	if err := d.conn.QueryRow(
		`SELECT status, repos_total, repos_synced, current_repo, current_tag
		   FROM sync_runs WHERE task_id = (SELECT id FROM sync_tasks WHERE name = 'legacy')`,
	).Scan(&status, &reposTotal, &synced, &currentRepo, &currentTag); err != nil {
		d.Close()
		t.Fatalf("select legacy run: %v", err)
	}
	if status != "running" {
		d.Close()
		t.Errorf("legacy run status = %q, want running", status)
	}
	if reposTotal != 5 || synced != 2 {
		d.Close()
		t.Errorf("legacy run counters mutated: total=%d synced=%d", reposTotal, synced)
	}
	if currentRepo != "" || currentTag != "" {
		d.Close()
		t.Errorf("legacy run progress = (%q,%q), want both empty", currentRepo, currentTag)
	}

	// Idempotency: a second Open must not re-run the ALTER (which would
	// fail with "duplicate column name" if the guard were broken).
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer d2.Close()
	if err := d2.conn.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("user_version after reopen: %v", err)
	}
	if v != 13 {
		t.Fatalf("user_version after reopen = %d, want 13", v)
	}
	if _, ok := tableColumns(t, d2.conn, "sync_runs")["current_repo"]; !ok {
		t.Fatal("current_repo missing after reopen")
	}
}

func TestSyncRunsCascadeOnTaskDelete(t *testing.T) {
	path := tempDBPath(t, "cascade.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	// The DSN sets _pragma=foreign_keys(1); cascade delete depends on it.
	var fk int
	if err := d.conn.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("PRAGMA foreign_keys = %d, want 1", fk)
	}

	res, err := d.conn.Exec(`INSERT INTO sync_tasks
		(name, direction, remote_url, include, enabled, created_at, updated_at)
		VALUES ('cascade', 'pull', 'http://192.0.2.1:10001', '', 1, 1, 1)`)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}
	taskID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("task id: %v", err)
	}

	if _, err := d.conn.Exec(`INSERT INTO sync_runs
		(task_id, started_at, status, repos_total, repos_synced, repos_failed, error)
		VALUES (?, 1, 'success', 1, 1, 0, '')`, taskID); err != nil {
		t.Fatalf("insert run: %v", err)
	}

	if _, err := d.conn.Exec("DELETE FROM sync_tasks WHERE id = ?", taskID); err != nil {
		t.Fatalf("delete task: %v", err)
	}

	var left int
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM sync_runs WHERE task_id = ?", taskID).Scan(&left); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if left != 0 {
		t.Fatalf("sync_runs rows after task delete = %d, want 0 (cascade)", left)
	}
}
