package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

const DefaultVerifiedCopies = 2

// Only recorded, verified backups belong to this policy. Loose database copies,
// workload backups, failed checks, and unverified archives are not removed.
func (m Manager) PruneVerified(ctx context.Context, keep int) (int, error) {
	if m.Directory == "" {
		return 0, errors.New("controller backup directory is not configured")
	}
	if keep < 2 {
		return 0, errors.New("retain at least two verified controller backups")
	}
	records, err := m.Store.ListBackupRecords(ctx)
	if reader, ok := m.Store.(interface {
		ListBackupRecordsForRetention(context.Context) ([]core.BackupRecord, error)
	}); ok {
		records, err = reader.ListBackupRecordsForRetention(ctx)
	}
	if err != nil {
		return 0, err
	}
	candidates := []core.BackupRecord{}
	for _, record := range records {
		if record.State == "verified" && record.VerifiedAt != nil {
			candidates = append(candidates, record)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].CreatedAt.Equal(candidates[j].CreatedAt) {
			return candidates[i].ID > candidates[j].ID
		}
		return candidates[i].CreatedAt.After(candidates[j].CreatedAt)
	})
	if len(candidates) <= keep {
		return 0, nil
	}
	// Check the copies being kept before deleting any older recovery copy.
	for _, record := range candidates[:keep] {
		if err = m.retentionArchive(record, true); err != nil {
			return 0, err
		}
	}
	removed := 0
	for _, record := range candidates[keep:] {
		if err = ctx.Err(); err != nil {
			return removed, err
		}
		if err = m.retentionArchive(record, false); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, err
		}
		if err = os.RemoveAll(filepath.Join(m.Directory, record.ID)); err != nil {
			return removed, err
		}
		record.State = "pruned"
		record.Bytes = 0
		record.Message = "Removed by controller backup retention. Two newer verified recovery copies are retained."
		if err = m.Store.SaveBackupRecord(ctx, record); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
func (m Manager) retentionArchive(record core.BackupRecord, checksums bool) error {
	if _, err := ulid.ParseStrict(record.ID); err != nil {
		return errors.New("invalid recorded backup identity")
	}
	dir := filepath.Join(m.Directory, record.ID)
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("recorded backup directory unavailable: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("recorded backup directory is unavailable")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return err
	}
	var meta manifest
	if json.Unmarshal(raw, &meta) != nil || meta.Engine != record.Engine || len(meta.Files) != 2 {
		return errors.New("recorded backup manifest is invalid")
	}
	name := "database.sqlite"
	if record.Engine == "postgresql" {
		name = "database.dump"
	}
	if meta.Files[name] == "" || meta.Files["master.key"] == "" {
		return errors.New("recorded backup files are invalid")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 3 {
		return errors.New("backup directory has additional files; cleanup skipped")
	}
	for _, entry := range entries {
		if entry.Name() != name && entry.Name() != "master.key" && entry.Name() != "manifest.json" {
			return errors.New("backup directory has unrecognized files")
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("backup file is not regular")
		}
	}
	if checksums {
		for _, name := range []string{name, "master.key"} {
			sum, _, err := checksum(filepath.Join(dir, name))
			if err != nil || sum != meta.Files[name] {
				return errors.New("retained backup checksum does not match; cleanup skipped")
			}
		}
	}
	return nil
}
