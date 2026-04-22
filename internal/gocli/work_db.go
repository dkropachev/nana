package gocli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const localWorkDBSchemaVersion = 2

type localWorkDBMigration struct {
	From  int
	To    int
	Apply func(*sql.DB) ([]string, error)
}

var localWorkDBMigrations = []localWorkDBMigration{
	{From: 0, To: 1, Apply: migrateLegacyLocalWorkDB},
	{From: 1, To: 2, Apply: migrateLocalWorkDBV2UsageSQLite},
}

type localWorkDBDiagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type localWorkDBCheckReport struct {
	DatabasePath         string                  `json:"database_path"`
	Exists               bool                    `json:"exists"`
	Empty                bool                    `json:"empty,omitempty"`
	SchemaVersion        int                     `json:"schema_version"`
	CurrentSchemaVersion int                     `json:"current_schema_version"`
	Healthy              bool                    `json:"healthy"`
	RepairRequired       bool                    `json:"repair_required,omitempty"`
	Diagnostics          []localWorkDBDiagnostic `json:"diagnostics,omitempty"`
}

type localWorkDBRepairReport struct {
	DatabasePath string                 `json:"database_path"`
	Exists       bool                   `json:"exists"`
	Changed      bool                   `json:"changed"`
	Actions      []string               `json:"actions,omitempty"`
	Check        localWorkDBCheckReport `json:"check"`
}

type localWorkDBSchemaError struct {
	Path           string
	SchemaVersion  int
	CurrentVersion int
}

func (e *localWorkDBSchemaError) Error() string {
	return fmt.Sprintf("local work DB schema at %s is version %d; run `nana work db-repair` to upgrade to version %d", e.Path, e.SchemaVersion, e.CurrentVersion)
}

func openLocalWorkSQLite(path string) (*sql.DB, error) {
	return openLocalWorkSQLiteWithProxy(path, true)
}

func openLocalWorkSQLiteDirect(path string) (*sql.DB, error) {
	return openLocalWorkSQLiteWithProxy(path, false)
}

func openLocalWorkSQLiteWithProxy(path string, allowProxy bool) (*sql.DB, error) {
	if !allowProxy || strings.TrimSpace(activeStartLocalWorkDBProxySocket()) != "" {
		return openLocalWorkSQLiteDriver("sqlite", path, false)
	}
	socketPath := localWorkDBProxySocketPath()
	present, err := localWorkDBProxySocketPresent(socketPath)
	if err != nil {
		return nil, err
	}
	if !present {
		return openLocalWorkSQLiteDriver("sqlite", path, false)
	}
	return openLocalWorkSQLiteDriver(localWorkDBProxyDriverName, socketPath, true)
}

func openLocalWorkSQLiteDriver(driverName string, dsn string, ping bool) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if ping {
		if err := db.Ping(); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("DB proxy socket present at %s, but connection failed: %w", dsn, err)
		}
	}
	return db, nil
}

func configureLocalWorkWritePragmas(db *sql.DB) error {
	statements := []string{
		`PRAGMA journal_mode=WAL;`,
		`PRAGMA foreign_keys=ON;`,
		`PRAGMA busy_timeout=5000;`,
		`PRAGMA synchronous=NORMAL;`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func configureLocalWorkReadPragmas(db *sql.DB) error {
	statements := []string{
		`PRAGMA foreign_keys=ON;`,
		`PRAGMA busy_timeout=15000;`,
		`PRAGMA query_only=ON;`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func localWorkCurrentSchemaDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS repos (
			repo_id TEXT PRIMARY KEY,
			repo_root TEXT NOT NULL UNIQUE,
			repo_name TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS runs (
			run_id TEXT PRIMARY KEY,
			repo_id TEXT NOT NULL,
			repo_root TEXT NOT NULL,
			repo_name TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			completed_at TEXT,
			status TEXT NOT NULL,
			current_phase TEXT,
			current_subphase TEXT,
			current_iteration INTEGER,
			current_round INTEGER,
			sandbox_path TEXT NOT NULL,
			sandbox_repo_path TEXT NOT NULL,
			manifest_json TEXT NOT NULL,
			FOREIGN KEY(repo_id) REFERENCES repos(repo_id) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_work_local_runs_repo_updated ON runs(repo_id, updated_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_work_local_runs_updated ON runs(updated_at DESC);`,
		`CREATE TABLE IF NOT EXISTS runtime_states (
			run_id TEXT NOT NULL,
			iteration INTEGER NOT NULL,
			state_json TEXT NOT NULL,
			PRIMARY KEY(run_id, iteration),
			FOREIGN KEY(run_id) REFERENCES runs(run_id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS finding_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id TEXT NOT NULL,
			event_json TEXT NOT NULL,
			FOREIGN KEY(run_id) REFERENCES runs(run_id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS work_run_index (
			run_id TEXT PRIMARY KEY,
			backend TEXT NOT NULL,
			repo_key TEXT,
			repo_root TEXT,
			repo_name TEXT,
			repo_slug TEXT,
			manifest_path TEXT,
			updated_at TEXT NOT NULL,
			target_kind TEXT
		);`,
		`CREATE INDEX IF NOT EXISTS idx_work_run_index_updated ON work_run_index(updated_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_work_run_index_backend_updated ON work_run_index(backend, updated_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_work_run_index_repo_updated ON work_run_index(repo_key, updated_at DESC);`,
		`CREATE TABLE IF NOT EXISTS work_items (
			id TEXT PRIMARY KEY CHECK(length(trim(id)) > 0),
			dedupe_key TEXT NOT NULL UNIQUE CHECK(length(trim(dedupe_key)) > 0),
			source TEXT NOT NULL CHECK(length(trim(source)) > 0),
			source_kind TEXT NOT NULL CHECK(length(trim(source_kind)) > 0),
			external_id TEXT NOT NULL CHECK(length(trim(external_id)) > 0),
			thread_key TEXT,
			repo_slug TEXT,
			target_url TEXT,
			linked_run_id TEXT,
			subject TEXT NOT NULL CHECK(length(trim(subject)) > 0),
			body TEXT,
			author TEXT,
			received_at TEXT NOT NULL CHECK(length(trim(received_at)) > 0),
			status TEXT NOT NULL CHECK(length(trim(status)) > 0),
			priority INTEGER NOT NULL DEFAULT 3 CHECK(priority BETWEEN 0 AND 5),
			auto_run INTEGER NOT NULL DEFAULT 0 CHECK(auto_run IN (0, 1)),
			auto_submit INTEGER NOT NULL DEFAULT 0 CHECK(auto_submit IN (0, 1)),
			hidden INTEGER NOT NULL DEFAULT 0 CHECK(hidden IN (0, 1)),
			hidden_reason TEXT,
			submit_profile_json TEXT,
			metadata_json TEXT,
			latest_draft_json TEXT,
			latest_artifact_root TEXT,
			latest_action_at TEXT,
			pause_reason TEXT,
			pause_until TEXT,
			created_at TEXT NOT NULL CHECK(length(trim(created_at)) > 0),
			updated_at TEXT NOT NULL CHECK(length(trim(updated_at)) > 0),
			FOREIGN KEY(linked_run_id) REFERENCES work_run_index(run_id) ON DELETE SET NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_work_items_status_updated ON work_items(status, updated_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_work_items_repo_updated ON work_items(repo_slug, updated_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_work_items_hidden_updated ON work_items(hidden, updated_at DESC);`,
		`CREATE TABLE IF NOT EXISTS work_item_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			item_id TEXT NOT NULL,
			created_at TEXT NOT NULL CHECK(length(trim(created_at)) > 0),
			event_type TEXT NOT NULL CHECK(length(trim(event_type)) > 0),
			actor TEXT,
			payload_json TEXT NOT NULL CHECK(length(trim(payload_json)) > 0),
			FOREIGN KEY(item_id) REFERENCES work_items(id) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_work_item_events_item_created ON work_item_events(item_id, created_at DESC, id DESC);`,
		`CREATE TABLE IF NOT EXISTS work_item_links (
			item_id TEXT NOT NULL,
			link_type TEXT NOT NULL CHECK(length(trim(link_type)) > 0),
			target_id TEXT NOT NULL CHECK(length(trim(target_id)) > 0),
			metadata_json TEXT NOT NULL CHECK(length(trim(metadata_json)) > 0),
			PRIMARY KEY(item_id, link_type, target_id),
			FOREIGN KEY(item_id) REFERENCES work_items(id) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_work_item_links_target ON work_item_links(link_type, target_id);`,
		`CREATE TABLE IF NOT EXISTS usage_sources (
			source_key TEXT PRIMARY KEY,
			source_kind TEXT NOT NULL,
			source_path TEXT NOT NULL,
			root TEXT NOT NULL,
			run_id TEXT,
			backend TEXT,
			sandbox_path TEXT,
			source_updated_at TEXT,
			size_bytes INTEGER NOT NULL DEFAULT 0,
			modified_unix_nano INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_usage_sources_path ON usage_sources(source_path);`,
		`CREATE INDEX IF NOT EXISTS idx_usage_sources_run_kind ON usage_sources(run_id, source_kind);`,
		`CREATE TABLE IF NOT EXISTS usage_sessions (
			session_key TEXT PRIMARY KEY,
			source_key TEXT NOT NULL,
			session_id TEXT,
			timestamp TEXT NOT NULL,
			timestamp_unix INTEGER NOT NULL DEFAULT 0,
			day TEXT NOT NULL,
			cwd TEXT,
			transcript_path TEXT NOT NULL,
			root TEXT NOT NULL,
			model TEXT,
			agent_role TEXT,
			agent_nickname TEXT,
			lane TEXT,
			activity TEXT NOT NULL,
			phase TEXT NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			cached_input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			reasoning_output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			has_token_usage INTEGER NOT NULL DEFAULT 0 CHECK(has_token_usage IN (0, 1)),
			started_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0,
			partial_window_coverage INTEGER NOT NULL DEFAULT 0 CHECK(partial_window_coverage IN (0, 1)),
			FOREIGN KEY(source_key) REFERENCES usage_sources(source_key) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_usage_sessions_root_timestamp ON usage_sessions(root, timestamp_unix DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_usage_sessions_source ON usage_sessions(source_key);`,
		`CREATE INDEX IF NOT EXISTS idx_usage_sessions_run_partial ON usage_sessions(partial_window_coverage, updated_at DESC);`,
		`CREATE TABLE IF NOT EXISTS usage_checkpoints (
			session_key TEXT NOT NULL,
			seq INTEGER NOT NULL,
			checkpoint_ts INTEGER NOT NULL,
			checkpoint_at TEXT NOT NULL,
			day TEXT NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			cached_input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			reasoning_output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			delta_input_tokens INTEGER NOT NULL DEFAULT 0,
			delta_cached_input_tokens INTEGER NOT NULL DEFAULT 0,
			delta_output_tokens INTEGER NOT NULL DEFAULT 0,
			delta_reasoning_output_tokens INTEGER NOT NULL DEFAULT 0,
			delta_total_tokens INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(session_key, seq),
			FOREIGN KEY(session_key) REFERENCES usage_sessions(session_key) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_usage_checkpoints_session_ts ON usage_checkpoints(session_key, checkpoint_ts);`,
		`CREATE INDEX IF NOT EXISTS idx_usage_checkpoints_day ON usage_checkpoints(day, checkpoint_ts);`,
		`CREATE TABLE IF NOT EXISTS usage_metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);`,
	}
}

func bootstrapLocalWorkDB(db *sql.DB) error {
	if err := configureLocalWorkWritePragmas(db); err != nil {
		return err
	}
	version, hasTables, err := localWorkDBSchemaState(db)
	if err != nil {
		return err
	}
	switch {
	case !hasTables:
		return localWorkCreateFreshSchema(db)
	case version == localWorkDBSchemaVersion:
		return ensureLocalWorkCurrentSchemaCompatibility(db)
	case version < localWorkDBSchemaVersion:
		_, err := applyLocalWorkDBMigrations(db, version)
		if err != nil {
			return err
		}
		return ensureLocalWorkCurrentSchemaCompatibility(db)
	default:
		return fmt.Errorf("local work DB schema at %s is version %d, newer than supported version %d", localWorkDBPath(), version, localWorkDBSchemaVersion)
	}
}

func ensureLocalWorkCurrentSchemaCompatibility(db *sql.DB) error {
	for _, stmt := range localWorkCurrentSchemaDDL() {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	if err := ensureSQLiteColumn(db, "work_run_index", "repo_slug", `ALTER TABLE work_run_index ADD COLUMN repo_slug TEXT`); err != nil {
		return err
	}
	if err := ensureSQLiteColumn(db, "work_items", "pause_reason", `ALTER TABLE work_items ADD COLUMN pause_reason TEXT`); err != nil {
		return err
	}
	if err := ensureSQLiteColumn(db, "work_items", "pause_until", `ALTER TABLE work_items ADD COLUMN pause_until TEXT`); err != nil {
		return err
	}
	return nil
}

func localWorkCreateFreshSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range localWorkCurrentSchemaDDL() {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, localWorkDBSchemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func createLocalWorkEmptyReadStore() (*localWorkDBStore, error) {
	db, err := openLocalWorkSQLite(":memory:")
	if err != nil {
		return nil, err
	}
	store := &localWorkDBStore{db: db}
	tx, err := db.Begin()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	for _, stmt := range localWorkCurrentSchemaDDL() {
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			return nil, err
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, localWorkDBSchemaVersion)); err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := configureLocalWorkReadPragmas(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func localWorkDBSchemaState(db *sql.DB) (int, bool, error) {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return 0, false, err
	}
	var tableCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&tableCount); err != nil {
		return 0, false, err
	}
	return version, tableCount > 0, nil
}

func localWorkTableExists(db *sql.DB, table string) (bool, error) {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func localWorkTableColumns(db *sql.DB, table string) (map[string]bool, error) {
	exists, err := localWorkTableExists(db, table)
	if err != nil {
		return nil, err
	}
	if !exists {
		return map[string]bool{}, nil
	}
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name string
		var ctype string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[strings.TrimSpace(strings.ToLower(name))] = true
	}
	return columns, rows.Err()
}

func normalizeLegacyWorkItemPauseStateDB(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, metadata_json, pause_reason, pause_until FROM work_items`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type update struct {
		id          string
		pauseReason string
		pauseUntil  string
		metadataRaw string
	}
	updates := []update{}
	for rows.Next() {
		var id string
		var metadataRaw sql.NullString
		var pauseReason sql.NullString
		var pauseUntil sql.NullString
		if err := rows.Scan(&id, &metadataRaw, &pauseReason, &pauseUntil); err != nil {
			return err
		}
		if strings.TrimSpace(pauseReason.String) != "" || strings.TrimSpace(pauseUntil.String) != "" {
			continue
		}
		if strings.TrimSpace(metadataRaw.String) == "" {
			continue
		}
		metadata := map[string]any{}
		if err := json.Unmarshal([]byte(metadataRaw.String), &metadata); err != nil {
			continue
		}
		legacyReason := metadataString(metadata, "pause_reason")
		legacyUntil := metadataString(metadata, "pause_until")
		if strings.TrimSpace(legacyReason) == "" && strings.TrimSpace(legacyUntil) == "" {
			continue
		}
		metadata = clearWorkItemPauseMetadata(metadata)
		nextMetadata, err := marshalNullableJSON(metadata)
		if err != nil {
			return err
		}
		updates = append(updates, update{
			id:          id,
			pauseReason: legacyReason,
			pauseUntil:  legacyUntil,
			metadataRaw: nextMetadata,
		})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, update := range updates {
		if _, err := db.Exec(`UPDATE work_items SET metadata_json = ?, pause_reason = ?, pause_until = ? WHERE id = ?`,
			nullableString(update.metadataRaw),
			nullableString(update.pauseReason),
			nullableString(update.pauseUntil),
			update.id,
		); err != nil {
			return err
		}
	}
	return nil
}

func migrateLegacyLocalWorkDB(db *sql.DB) ([]string, error) {
	actions := []string{
		fmt.Sprintf("migrated legacy SQLite schema to version %d", localWorkDBSchemaVersion),
	}
	if err := ensureSQLiteColumn(db, "work_run_index", "repo_slug", `ALTER TABLE work_run_index ADD COLUMN repo_slug TEXT`); err != nil {
		return actions, err
	}
	if err := ensureSQLiteColumn(db, "work_items", "pause_reason", `ALTER TABLE work_items ADD COLUMN pause_reason TEXT`); err != nil {
		return actions, err
	}
	if err := ensureSQLiteColumn(db, "work_items", "pause_until", `ALTER TABLE work_items ADD COLUMN pause_until TEXT`); err != nil {
		return actions, err
	}
	if err := normalizeLegacyWorkItemPauseStateDB(db); err != nil {
		return actions, err
	}

	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return actions, err
	}
	tx, err := db.Begin()
	if err != nil {
		return actions, err
	}
	defer tx.Rollback()

	statements := []string{
		`DROP TABLE IF EXISTS runtime_states_new;`,
		`DROP TABLE IF EXISTS finding_history_new;`,
		`DROP TABLE IF EXISTS work_items_new;`,
		`DROP TABLE IF EXISTS work_item_events_new;`,
		`DROP TABLE IF EXISTS work_item_links_new;`,
		`CREATE TABLE runtime_states_new (
			run_id TEXT NOT NULL,
			iteration INTEGER NOT NULL,
			state_json TEXT NOT NULL,
			PRIMARY KEY(run_id, iteration),
			FOREIGN KEY(run_id) REFERENCES runs(run_id) ON DELETE CASCADE
		);`,
		`CREATE TABLE finding_history_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id TEXT NOT NULL,
			event_json TEXT NOT NULL,
			FOREIGN KEY(run_id) REFERENCES runs(run_id) ON DELETE CASCADE
		);`,
		`CREATE TABLE work_items_new (
			id TEXT PRIMARY KEY CHECK(length(trim(id)) > 0),
			dedupe_key TEXT NOT NULL UNIQUE CHECK(length(trim(dedupe_key)) > 0),
			source TEXT NOT NULL CHECK(length(trim(source)) > 0),
			source_kind TEXT NOT NULL CHECK(length(trim(source_kind)) > 0),
			external_id TEXT NOT NULL CHECK(length(trim(external_id)) > 0),
			thread_key TEXT,
			repo_slug TEXT,
			target_url TEXT,
			linked_run_id TEXT,
			subject TEXT NOT NULL CHECK(length(trim(subject)) > 0),
			body TEXT,
			author TEXT,
			received_at TEXT NOT NULL CHECK(length(trim(received_at)) > 0),
			status TEXT NOT NULL CHECK(length(trim(status)) > 0),
			priority INTEGER NOT NULL DEFAULT 3 CHECK(priority BETWEEN 0 AND 5),
			auto_run INTEGER NOT NULL DEFAULT 0 CHECK(auto_run IN (0, 1)),
			auto_submit INTEGER NOT NULL DEFAULT 0 CHECK(auto_submit IN (0, 1)),
			hidden INTEGER NOT NULL DEFAULT 0 CHECK(hidden IN (0, 1)),
			hidden_reason TEXT,
			submit_profile_json TEXT,
			metadata_json TEXT,
			latest_draft_json TEXT,
			latest_artifact_root TEXT,
			latest_action_at TEXT,
			pause_reason TEXT,
			pause_until TEXT,
			created_at TEXT NOT NULL CHECK(length(trim(created_at)) > 0),
			updated_at TEXT NOT NULL CHECK(length(trim(updated_at)) > 0),
			FOREIGN KEY(linked_run_id) REFERENCES work_run_index(run_id) ON DELETE SET NULL
		);`,
		`CREATE TABLE work_item_events_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			item_id TEXT NOT NULL,
			created_at TEXT NOT NULL CHECK(length(trim(created_at)) > 0),
			event_type TEXT NOT NULL CHECK(length(trim(event_type)) > 0),
			actor TEXT,
			payload_json TEXT NOT NULL CHECK(length(trim(payload_json)) > 0),
			FOREIGN KEY(item_id) REFERENCES work_items_new(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE work_item_links_new (
			item_id TEXT NOT NULL,
			link_type TEXT NOT NULL CHECK(length(trim(link_type)) > 0),
			target_id TEXT NOT NULL CHECK(length(trim(target_id)) > 0),
			metadata_json TEXT NOT NULL CHECK(length(trim(metadata_json)) > 0),
			PRIMARY KEY(item_id, link_type, target_id),
			FOREIGN KEY(item_id) REFERENCES work_items_new(id) ON DELETE CASCADE
		);`,
		`INSERT INTO runtime_states_new(run_id, iteration, state_json)
		 SELECT runtime_states.run_id, runtime_states.iteration, runtime_states.state_json
		   FROM runtime_states
		  WHERE EXISTS (SELECT 1 FROM runs WHERE runs.run_id = runtime_states.run_id);`,
		`INSERT INTO finding_history_new(id, run_id, event_json)
		 SELECT finding_history.id, finding_history.run_id, finding_history.event_json
		   FROM finding_history
		  WHERE EXISTS (SELECT 1 FROM runs WHERE runs.run_id = finding_history.run_id)
		  ORDER BY finding_history.id;`,
		`INSERT INTO work_items_new(
			id, dedupe_key, source, source_kind, external_id, thread_key, repo_slug, target_url, linked_run_id,
			subject, body, author, received_at, status, priority, auto_run, auto_submit, hidden, hidden_reason,
			submit_profile_json, metadata_json, latest_draft_json, latest_artifact_root, latest_action_at,
			pause_reason, pause_until, created_at, updated_at
		)
		SELECT
			work_items.id,
			work_items.dedupe_key,
			work_items.source,
			work_items.source_kind,
			work_items.external_id,
			NULLIF(work_items.thread_key, ''),
			NULLIF(work_items.repo_slug, ''),
			NULLIF(work_items.target_url, ''),
			CASE
				WHEN work_items.linked_run_id IS NOT NULL AND EXISTS (SELECT 1 FROM work_run_index WHERE work_run_index.run_id = work_items.linked_run_id)
					THEN work_items.linked_run_id
				ELSE NULL
			END,
			work_items.subject,
			NULLIF(work_items.body, ''),
			NULLIF(work_items.author, ''),
			work_items.received_at,
			work_items.status,
			CASE WHEN work_items.priority BETWEEN 0 AND 5 THEN work_items.priority ELSE 3 END,
			CASE WHEN work_items.auto_run <> 0 THEN 1 ELSE 0 END,
			CASE WHEN work_items.auto_submit <> 0 THEN 1 ELSE 0 END,
			CASE WHEN work_items.hidden <> 0 THEN 1 ELSE 0 END,
			NULLIF(work_items.hidden_reason, ''),
			work_items.submit_profile_json,
			work_items.metadata_json,
			work_items.latest_draft_json,
			NULLIF(work_items.latest_artifact_root, ''),
			NULLIF(work_items.latest_action_at, ''),
			NULLIF(work_items.pause_reason, ''),
			NULLIF(work_items.pause_until, ''),
			work_items.created_at,
			work_items.updated_at
		FROM work_items
		WHERE trim(work_items.id) <> ''
		  AND trim(work_items.dedupe_key) <> ''
		  AND trim(work_items.source) <> ''
		  AND trim(work_items.source_kind) <> ''
		  AND trim(work_items.external_id) <> ''
		  AND trim(work_items.subject) <> ''
		  AND trim(work_items.received_at) <> ''
		  AND trim(work_items.status) <> ''
		  AND trim(work_items.created_at) <> ''
		  AND trim(work_items.updated_at) <> '';`,
		`INSERT INTO work_item_events_new(id, item_id, created_at, event_type, actor, payload_json)
		 SELECT work_item_events.id, work_item_events.item_id, work_item_events.created_at, work_item_events.event_type, work_item_events.actor, work_item_events.payload_json
		   FROM work_item_events
		  WHERE EXISTS (SELECT 1 FROM work_items_new WHERE work_items_new.id = work_item_events.item_id)
		  ORDER BY work_item_events.id;`,
		`INSERT INTO work_item_links_new(item_id, link_type, target_id, metadata_json)
		 SELECT work_item_links.item_id, work_item_links.link_type, work_item_links.target_id, work_item_links.metadata_json
		   FROM work_item_links
		  WHERE EXISTS (SELECT 1 FROM work_items_new WHERE work_items_new.id = work_item_links.item_id);`,
		`DROP TABLE runtime_states;`,
		`DROP TABLE finding_history;`,
		`DROP TABLE work_item_events;`,
		`DROP TABLE work_item_links;`,
		`DROP TABLE work_items;`,
		`ALTER TABLE runtime_states_new RENAME TO runtime_states;`,
		`ALTER TABLE finding_history_new RENAME TO finding_history;`,
		`ALTER TABLE work_items_new RENAME TO work_items;`,
		`ALTER TABLE work_item_events_new RENAME TO work_item_events;`,
		`ALTER TABLE work_item_links_new RENAME TO work_item_links;`,
		`CREATE INDEX idx_work_items_status_updated ON work_items(status, updated_at DESC);`,
		`CREATE INDEX idx_work_items_repo_updated ON work_items(repo_slug, updated_at DESC);`,
		`CREATE INDEX idx_work_items_hidden_updated ON work_items(hidden, updated_at DESC);`,
		`CREATE INDEX idx_work_item_events_item_created ON work_item_events(item_id, created_at DESC, id DESC);`,
		`CREATE INDEX idx_work_item_links_target ON work_item_links(link_type, target_id);`,
		fmt.Sprintf(`PRAGMA user_version=%d;`, localWorkDBSchemaVersion),
	}
	for _, stmt := range statements {
		if _, err := tx.Exec(stmt); err != nil {
			return actions, err
		}
	}
	if err := tx.Commit(); err != nil {
		return actions, err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return actions, err
	}
	return actions, nil
}

func migrateLocalWorkDBV2UsageSQLite(db *sql.DB) ([]string, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, stmt := range localWorkCurrentSchemaDDL() {
		if _, err := tx.Exec(stmt); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, localWorkDBSchemaVersion)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return []string{fmt.Sprintf("migrated SQLite schema to version %d", localWorkDBSchemaVersion)}, nil
}

func inspectLocalWorkDB() (localWorkDBCheckReport, error) {
	report := localWorkDBCheckReport{
		DatabasePath:         localWorkDBPath(),
		CurrentSchemaVersion: localWorkDBSchemaVersion,
		Healthy:              true,
	}
	if _, err := os.Stat(report.DatabasePath); err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, err
	}
	report.Exists = true
	db, err := openLocalWorkSQLite(report.DatabasePath)
	if err != nil {
		return report, err
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return report, err
	}
	version, hasTables, err := localWorkDBSchemaState(db)
	if err != nil {
		return report, err
	}
	report.SchemaVersion = version
	if !hasTables {
		report.Empty = true
		return report, nil
	}
	if version == 0 {
		report.RepairRequired = true
		report.Diagnostics = append(report.Diagnostics, localWorkDBDiagnostic{
			Code:     "schema_version",
			Severity: "warn",
			Message:  fmt.Sprintf("schema version %d is older than current version %d; run `nana work db-repair`", version, localWorkDBSchemaVersion),
		})
	} else if version < localWorkDBSchemaVersion {
		report.RepairRequired = true
		report.Diagnostics = append(report.Diagnostics, localWorkDBDiagnostic{
			Code:     "schema_version",
			Severity: "warn",
			Message:  fmt.Sprintf("schema version %d is older than current version %d; run `nana work db-repair`", version, localWorkDBSchemaVersion),
		})
	} else if version > localWorkDBSchemaVersion {
		report.Healthy = false
		report.Diagnostics = append(report.Diagnostics, localWorkDBDiagnostic{
			Code:     "schema_version",
			Severity: "fail",
			Message:  fmt.Sprintf("schema version %d is newer than supported version %d", version, localWorkDBSchemaVersion),
		})
	}

	integrityRows, err := collectSingleColumnPragma(db, `PRAGMA integrity_check`)
	if err != nil {
		return report, err
	}
	if len(integrityRows) == 0 || !(len(integrityRows) == 1 && strings.EqualFold(strings.TrimSpace(integrityRows[0]), "ok")) {
		report.Healthy = false
		report.Diagnostics = append(report.Diagnostics, localWorkDBDiagnostic{
			Code:     "integrity_check",
			Severity: "fail",
			Message:  fmt.Sprintf("integrity check failed: %s", strings.Join(integrityRows, "; ")),
		})
	} else {
		report.Diagnostics = append(report.Diagnostics, localWorkDBDiagnostic{
			Code:     "integrity_check",
			Severity: "pass",
			Message:  "integrity check passed",
		})
	}

	foreignKeyRows, err := collectForeignKeyCheckRows(db)
	if err != nil {
		return report, err
	}
	if len(foreignKeyRows) > 0 {
		report.Healthy = false
		report.RepairRequired = true
		report.Diagnostics = append(report.Diagnostics, localWorkDBDiagnostic{
			Code:     "foreign_key_check",
			Severity: "fail",
			Message:  fmt.Sprintf("foreign key check failed: %s", strings.Join(foreignKeyRows, "; ")),
		})
	} else {
		report.Diagnostics = append(report.Diagnostics, localWorkDBDiagnostic{
			Code:     "foreign_key_check",
			Severity: "pass",
			Message:  "foreign key check passed",
		})
	}

	logicalDiagnostics, err := collectLocalWorkDBLogicalDiagnostics(db)
	if err != nil {
		return report, err
	}
	for _, diagnostic := range logicalDiagnostics {
		if diagnostic.Severity == "fail" {
			report.Healthy = false
		}
		if diagnostic.Severity == "warn" {
			report.RepairRequired = true
		}
		report.Diagnostics = append(report.Diagnostics, diagnostic)
	}
	return report, nil
}

func collectSingleColumnPragma(db *sql.DB, query string) ([]string, error) {
	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, strings.TrimSpace(value))
	}
	return values, rows.Err()
}

func collectForeignKeyCheckRows(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var table string
		var rowID int64
		var parent string
		var fkid int64
		if err := rows.Scan(&table, &rowID, &parent, &fkid); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s rowid=%d parent=%s fkid=%d", table, rowID, parent, fkid))
	}
	return out, rows.Err()
}

func collectLocalWorkDBLogicalDiagnostics(db *sql.DB) ([]localWorkDBDiagnostic, error) {
	diagnostics := []localWorkDBDiagnostic{}
	checks := []struct {
		table string
		code  string
		query string
		label string
	}{
		{
			table: "runtime_states",
			code:  "orphan_runtime_states",
			query: `SELECT COUNT(*) FROM runtime_states WHERE NOT EXISTS (SELECT 1 FROM runs WHERE runs.run_id = runtime_states.run_id)`,
			label: "runtime state row(s) reference missing runs",
		},
		{
			table: "finding_history",
			code:  "orphan_finding_history",
			query: `SELECT COUNT(*) FROM finding_history WHERE NOT EXISTS (SELECT 1 FROM runs WHERE runs.run_id = finding_history.run_id)`,
			label: "finding-history row(s) reference missing runs",
		},
		{
			table: "work_item_events",
			code:  "orphan_work_item_events",
			query: `SELECT COUNT(*) FROM work_item_events WHERE NOT EXISTS (SELECT 1 FROM work_items WHERE work_items.id = work_item_events.item_id)`,
			label: "work-item event row(s) reference missing items",
		},
		{
			table: "work_item_links",
			code:  "orphan_work_item_links",
			query: `SELECT COUNT(*) FROM work_item_links WHERE NOT EXISTS (SELECT 1 FROM work_items WHERE work_items.id = work_item_links.item_id)`,
			label: "work-item link row(s) reference missing items",
		},
		{
			table: "work_items",
			code:  "dangling_linked_run_id",
			query: `SELECT COUNT(*) FROM work_items WHERE linked_run_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM work_run_index WHERE work_run_index.run_id = work_items.linked_run_id)`,
			label: "work item row(s) point at missing linked runs",
		},
		{
			table: "work_items",
			code:  "invalid_work_item_priority",
			query: `SELECT COUNT(*) FROM work_items WHERE priority NOT BETWEEN 0 AND 5`,
			label: "work item row(s) have invalid priority values",
		},
		{
			table: "work_items",
			code:  "invalid_work_item_flags",
			query: `SELECT COUNT(*) FROM work_items WHERE auto_run NOT IN (0, 1) OR auto_submit NOT IN (0, 1) OR hidden NOT IN (0, 1)`,
			label: "work item row(s) have invalid boolean flags",
		},
		{
			table: "work_items",
			code:  "invalid_work_item_required_text",
			query: `SELECT COUNT(*) FROM work_items WHERE trim(id) = '' OR trim(dedupe_key) = '' OR trim(source) = '' OR trim(source_kind) = '' OR trim(external_id) = '' OR trim(subject) = '' OR trim(received_at) = '' OR trim(status) = '' OR trim(created_at) = '' OR trim(updated_at) = ''`,
			label: "work item row(s) have empty required text fields",
		},
	}
	for _, check := range checks {
		exists, err := localWorkTableExists(db, check.table)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		var count int
		if err := db.QueryRow(check.query).Scan(&count); err != nil {
			return nil, err
		}
		if count == 0 {
			continue
		}
		diagnostics = append(diagnostics, localWorkDBDiagnostic{
			Code:     check.code,
			Severity: "warn",
			Message:  fmt.Sprintf("%d %s", count, check.label),
		})
	}
	return diagnostics, nil
}

func repairLocalWorkDB() (localWorkDBRepairReport, error) {
	report := localWorkDBRepairReport{
		DatabasePath: localWorkDBPath(),
		Check: localWorkDBCheckReport{
			DatabasePath:         localWorkDBPath(),
			CurrentSchemaVersion: localWorkDBSchemaVersion,
			Healthy:              true,
		},
	}
	if _, err := os.Stat(report.DatabasePath); err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, err
	}
	report.Exists = true
	db, err := openLocalWorkSQLite(report.DatabasePath)
	if err != nil {
		return report, err
	}
	defer db.Close()
	if err := configureLocalWorkWritePragmas(db); err != nil {
		return report, err
	}

	actions, err := bootstrapAndRepairLocalWorkDB(db)
	report.Actions = actions
	report.Changed = len(actions) > 0
	if err != nil {
		return report, err
	}
	check, err := inspectLocalWorkDB()
	if err != nil {
		return report, err
	}
	report.Check = check
	if !check.Healthy {
		return report, fmt.Errorf("local work DB repair did not converge")
	}
	return report, nil
}

func bootstrapAndRepairLocalWorkDB(db *sql.DB) ([]string, error) {
	actions := []string{}
	version, hasTables, err := localWorkDBSchemaState(db)
	if err != nil {
		return actions, err
	}
	switch {
	case !hasTables:
		if err := localWorkCreateFreshSchema(db); err != nil {
			return actions, err
		}
		actions = append(actions, fmt.Sprintf("initialized SQLite schema version %d", localWorkDBSchemaVersion))
	case version == localWorkDBSchemaVersion:
	case version < localWorkDBSchemaVersion:
		migrationActions, err := applyLocalWorkDBMigrations(db, version)
		actions = append(actions, migrationActions...)
		if err != nil {
			return actions, err
		}
	default:
		return actions, fmt.Errorf("local work DB schema at %s is version %d, newer than supported version %d", localWorkDBPath(), version, localWorkDBSchemaVersion)
	}
	if err := ensureLocalWorkCurrentSchemaCompatibility(db); err != nil {
		return actions, err
	}
	repairActions, err := repairCurrentLocalWorkDB(db)
	actions = append(actions, repairActions...)
	return actions, err
}

func applyLocalWorkDBMigrations(db *sql.DB, version int) ([]string, error) {
	actions := []string{}
	current := version
	for current < localWorkDBSchemaVersion {
		migration, ok := localWorkDBMigrationFrom(current)
		if !ok {
			return actions, &localWorkDBSchemaError{
				Path:           localWorkDBPath(),
				SchemaVersion:  current,
				CurrentVersion: localWorkDBSchemaVersion,
			}
		}
		migrationActions, err := migration.Apply(db)
		actions = append(actions, migrationActions...)
		if err != nil {
			return actions, err
		}
		current = migration.To
	}
	return actions, nil
}

func localWorkDBMigrationFrom(version int) (localWorkDBMigration, bool) {
	for _, migration := range localWorkDBMigrations {
		if migration.From == version {
			return migration, true
		}
	}
	return localWorkDBMigration{}, false
}

func asLocalWorkDBSchemaError(err error) (*localWorkDBSchemaError, bool) {
	if err == nil {
		return nil, false
	}
	var schemaErr *localWorkDBSchemaError
	if errors.As(err, &schemaErr) {
		return schemaErr, true
	}
	return nil, false
}

func localWorkReadCommandError(err error) error {
	if _, ok := asLocalWorkDBSchemaError(err); ok {
		return fmt.Errorf("local work DB requires repair; run `nana work db-repair`")
	}
	return err
}

func repairCurrentLocalWorkDB(db *sql.DB) ([]string, error) {
	actions := []string{}
	tx, err := db.Begin()
	if err != nil {
		return actions, err
	}
	defer tx.Rollback()

	mutations := []struct {
		label string
		query string
	}{
		{
			label: "normalized invalid work-item priorities",
			query: `UPDATE work_items SET priority = 3 WHERE priority NOT BETWEEN 0 AND 5`,
		},
		{
			label: "normalized invalid work-item boolean flags",
			query: `UPDATE work_items
			           SET auto_run = CASE WHEN auto_run <> 0 THEN 1 ELSE 0 END,
			               auto_submit = CASE WHEN auto_submit <> 0 THEN 1 ELSE 0 END,
			               hidden = CASE WHEN hidden <> 0 THEN 1 ELSE 0 END
			         WHERE auto_run NOT IN (0, 1) OR auto_submit NOT IN (0, 1) OR hidden NOT IN (0, 1)`,
		},
		{
			label: "cleared dangling linked_run_id references",
			query: `UPDATE work_items
			           SET linked_run_id = NULL
			         WHERE linked_run_id IS NOT NULL
			           AND NOT EXISTS (SELECT 1 FROM work_run_index WHERE work_run_index.run_id = work_items.linked_run_id)`,
		},
		{
			label: "removed orphan runtime_states rows",
			query: `DELETE FROM runtime_states
			         WHERE NOT EXISTS (SELECT 1 FROM runs WHERE runs.run_id = runtime_states.run_id)`,
		},
		{
			label: "removed orphan finding_history rows",
			query: `DELETE FROM finding_history
			         WHERE NOT EXISTS (SELECT 1 FROM runs WHERE runs.run_id = finding_history.run_id)`,
		},
		{
			label: "removed orphan work_item_events rows",
			query: `DELETE FROM work_item_events
			         WHERE NOT EXISTS (SELECT 1 FROM work_items WHERE work_items.id = work_item_events.item_id)`,
		},
		{
			label: "removed orphan work_item_links rows",
			query: `DELETE FROM work_item_links
			         WHERE NOT EXISTS (SELECT 1 FROM work_items WHERE work_items.id = work_item_links.item_id)`,
		},
	}
	for _, mutation := range mutations {
		result, err := tx.Exec(mutation.query)
		if err != nil {
			if isMissingLocalWorkRepairTableError(err) {
				continue
			}
			return actions, err
		}
		rowsAffected, err := result.RowsAffected()
		if err == nil && rowsAffected > 0 {
			actions = append(actions, fmt.Sprintf("%s (%d row(s))", mutation.label, rowsAffected))
		}
	}
	if err := tx.Commit(); err != nil {
		return actions, err
	}
	return actions, nil
}

func isMissingLocalWorkRepairTableError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "no such table")
}

func runWorkDBCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("Usage: nana work db-check [--json]\n       nana work db-repair [--json]\n\n%s", WorkHelp)
	}
	subcommand := strings.TrimSpace(args[0])
	jsonOutput, err := parseLocalWorkDBJSONArgs(args[1:], subcommand)
	if err != nil {
		return err
	}
	switch subcommand {
	case "db-check":
		return runWorkDBCheck(jsonOutput)
	case "db-repair":
		return runWorkDBRepair(jsonOutput)
	default:
		return fmt.Errorf("Usage: nana work db-check [--json]\n       nana work db-repair [--json]\n\n%s", WorkHelp)
	}
}

func parseLocalWorkDBJSONArgs(args []string, subcommand string) (bool, error) {
	jsonOutput := false
	for _, token := range args {
		if token == "--json" {
			jsonOutput = true
			continue
		}
		return false, fmt.Errorf("Usage: nana work %s [--json]\n\n%s", subcommand, WorkHelp)
	}
	return jsonOutput, nil
}

func runWorkDBCheck(jsonOutput bool) error {
	report, err := inspectLocalWorkDB()
	if err != nil {
		return err
	}
	if jsonOutput {
		if err := writeIndentedJSON(report); err != nil {
			return err
		}
	} else {
		printLocalWorkDBCheckReport(report)
	}
	if !report.Healthy || report.RepairRequired {
		return errors.New("work DB check found issues")
	}
	return nil
}

func runWorkDBRepair(jsonOutput bool) error {
	report, err := repairLocalWorkDB()
	if jsonOutput {
		if writeErr := writeIndentedJSON(report); writeErr != nil {
			return writeErr
		}
	} else {
		printLocalWorkDBRepairReport(report)
	}
	if err != nil {
		return err
	}
	if !report.Check.Healthy {
		return errors.New("work DB repair did not resolve all issues")
	}
	return nil
}

func printLocalWorkDBCheckReport(report localWorkDBCheckReport) {
	fmt.Fprintf(os.Stdout, "[work-db] Path: %s\n", report.DatabasePath)
	if !report.Exists {
		fmt.Fprintln(os.Stdout, "[work-db] State DB: not created yet")
		return
	}
	fmt.Fprintf(os.Stdout, "[work-db] Schema version: %d (current=%d)\n", report.SchemaVersion, report.CurrentSchemaVersion)
	if report.Empty {
		fmt.Fprintln(os.Stdout, "[work-db] Database is empty.")
		return
	}
	for _, diagnostic := range report.Diagnostics {
		prefix := "[OK]"
		switch diagnostic.Severity {
		case "warn":
			prefix = "[!!]"
		case "fail":
			prefix = "[XX]"
		}
		fmt.Fprintf(os.Stdout, "[work-db] %s %s: %s\n", prefix, diagnostic.Code, diagnostic.Message)
	}
}

func printLocalWorkDBRepairReport(report localWorkDBRepairReport) {
	fmt.Fprintf(os.Stdout, "[work-db] Path: %s\n", report.DatabasePath)
	if !report.Exists {
		fmt.Fprintln(os.Stdout, "[work-db] State DB: not created yet")
		return
	}
	if len(report.Actions) == 0 {
		fmt.Fprintln(os.Stdout, "[work-db] No repair actions were required.")
	} else {
		for _, action := range report.Actions {
			fmt.Fprintf(os.Stdout, "[work-db] Repaired: %s\n", action)
		}
	}
	printLocalWorkDBCheckReport(report.Check)
}
