package api

import (
	"context"
	"net/http"
	"time"

	"github.com/doout/dispatch/internal/backup"
	"github.com/go-chi/chi/v5"
)

func (a *API) listBackups(w http.ResponseWriter, r *http.Request) {
	data, ok := a.ops(w)
	if !ok {
		return
	}
	records, err := data.ListBackupRecords(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"configured": a.eventConfig.BackupDirectory != "" && a.eventConfig.MasterKeyFile != "", "backups": records})
}
func (a *API) createBackup(w http.ResponseWriter, r *http.Request) { a.runBackup(w, r, false) }
func (a *API) verifyBackup(w http.ResponseWriter, r *http.Request) { a.runBackup(w, r, true) }
func (a *API) runBackup(w http.ResponseWriter, r *http.Request, verify bool) {
	data, ok := a.store.(backup.Repository)
	if !ok || a.eventConfig.BackupDirectory == "" || a.eventConfig.MasterKeyFile == "" {
		problem(w, 503, "Backups unavailable", "Configure a controller master key and backup directory.")
		return
	}
	if !a.backupMu.TryLock() {
		problem(w, 409, "Backup already running", "Wait for the active backup or restore check.")
		return
	}
	defer a.backupMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	manager := backup.Manager{Store: data, Directory: a.eventConfig.BackupDirectory, MasterKeyFile: a.eventConfig.MasterKeyFile, DatabaseURL: a.eventConfig.DatabaseURL}
	if verify {
		record, err := manager.Verify(ctx, chi.URLParam(r, "id"))
		if err != nil {
			problem(w, 422, "Restore check failed", "The backup could not be restored and verified. Check the backup record and database permissions.")
			return
		}
		writeJSON(w, 200, record)
	} else {
		record, err := manager.Create(ctx)
		if err != nil {
			problem(w, 422, "Backup failed", "Check storage space, the vault key, and database backup tool permissions.")
			return
		}
		writeJSON(w, 201, record)
	}
}
