package gocli

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func dropWorkRunByID(runID string) error {
	entry, err := readWorkRunIndex(runID)
	if err != nil {
		return err
	}
	switch strings.TrimSpace(entry.Backend) {
	case "local":
		return dropLocalWorkRun(entry)
	case "github":
		return dropGithubWorkRun(entry)
	default:
		return fmt.Errorf("unsupported work run backend %q", entry.Backend)
	}
}

func dropLocalWorkRun(entry workRunIndexEntry) error {
	repoID := strings.TrimSpace(entry.RepoKey)
	runDir := ""
	sandboxPath := ""
	if manifest, err := readLocalWorkManifestByRunID(entry.RunID); err == nil {
		repoID = defaultString(strings.TrimSpace(manifest.RepoID), repoID)
		runDir = localWorkRunDirByID(manifest.RepoID, manifest.RunID)
		sandboxPath = strings.TrimSpace(manifest.SandboxPath)
	} else if repoID != "" {
		runDir = localWorkRunDirByID(repoID, entry.RunID)
	}

	if err := withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		tx, err := store.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()

		now := ISOTimeNow()
		if err := detachWorkItemsFromRunTx(tx, entry.RunID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM runtime_states WHERE run_id = ?`, entry.RunID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM finding_history WHERE run_id = ?`, entry.RunID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM runs WHERE run_id = ?`, entry.RunID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM work_run_index WHERE run_id = ?`, entry.RunID); err != nil {
			return err
		}
		if repoID != "" {
			if _, err := tx.Exec(`DELETE FROM repos WHERE repo_id = ? AND NOT EXISTS (SELECT 1 FROM runs WHERE repo_id = ?)`, repoID, repoID); err != nil {
				return err
			}
		}
		return tx.Commit()
	}); err != nil {
		return err
	}

	if _, err := removePathIfExists(runDir); err != nil {
		return err
	}
	if _, err := pruneEmptyParentDirs(runDir, localWorkReposDir()); err != nil {
		return err
	}
	if _, err := removePathIfExists(sandboxPath); err != nil {
		return err
	}
	if _, err := pruneEmptyParentDirs(sandboxPath, localWorkSandboxesDir()); err != nil {
		return err
	}
	return nil
}

func dropGithubWorkRun(entry workRunIndexEntry) error {
	manifestPath := strings.TrimSpace(entry.ManifestPath)
	runDir := ""
	if manifestPath != "" {
		runDir = filepath.Dir(manifestPath)
	}
	repoRoot := strings.TrimSpace(entry.RepoRoot)
	sandboxPath := ""
	if manifestPath != "" {
		if manifest, err := readGithubWorkManifest(manifestPath); err == nil {
			repoRoot = defaultString(strings.TrimSpace(manifest.ManagedRepoRoot), repoRoot)
			sandboxPath = strings.TrimSpace(manifest.SandboxPath)
		}
	}
	if repoRoot == "" && runDir != "" {
		repoRoot = filepath.Dir(filepath.Dir(runDir))
	}

	if err := withLocalWorkWriteStoreErr(func(store *localWorkDBStore) error {
		tx, err := store.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()

		now := ISOTimeNow()
		if err := detachWorkItemsFromRunTx(tx, entry.RunID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM work_run_index WHERE run_id = ?`, entry.RunID); err != nil {
			return err
		}
		return tx.Commit()
	}); err != nil {
		return err
	}

	if repoRoot != "" {
		if err := clearGithubLatestRunPointerForRun(filepath.Join(repoRoot, "latest-run.json"), repoRoot, entry.RunID); err != nil {
			return err
		}
		if err := clearGithubLatestRunPointerForRun(githubWorkLatestRunPath(), repoRoot, entry.RunID); err != nil {
			return err
		}
	}
	if _, err := removePathIfExists(runDir); err != nil {
		return err
	}
	if _, err := pruneEmptyParentDirs(runDir, repoRoot); err != nil {
		return err
	}
	if _, err := removePathIfExists(sandboxPath); err != nil {
		return err
	}
	if _, err := pruneEmptyParentDirs(sandboxPath, repoRoot); err != nil {
		return err
	}
	return nil
}

func detachWorkItemsFromRunTx(tx *sql.Tx, runID string, updatedAt string) error {
	if _, err := tx.Exec(`UPDATE work_items SET linked_run_id = NULL, updated_at = ? WHERE linked_run_id = ?`, updatedAt, runID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM work_item_links WHERE link_type = 'run' AND target_id = ?`, runID); err != nil {
		return err
	}
	return nil
}

func clearGithubLatestRunPointerForRun(path string, repoRoot string, runID string) error {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return nil
	}
	var pointer githubLatestRunPointer
	if err := readGithubJSON(cleanPath, &pointer); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if filepath.Clean(strings.TrimSpace(pointer.RepoRoot)) != filepath.Clean(strings.TrimSpace(repoRoot)) {
		return nil
	}
	if strings.TrimSpace(pointer.RunID) != strings.TrimSpace(runID) {
		return nil
	}
	if err := os.Remove(cleanPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
