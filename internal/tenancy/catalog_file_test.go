package tenancy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogRejectsPublicFilesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "public.sqlite")
	if err := os.WriteFile(path, []byte("unchanged"), 0644); err != nil {
		t.Fatal(err)
	}
	if catalog, err := Open(context.Background(), path); err == nil {
		catalog.Close()
		t.Fatal("public catalog accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked.sqlite")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if catalog, err := Open(context.Background(), link); err == nil {
		catalog.Close()
		t.Fatal("catalog symlink accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "unchanged" {
		t.Fatal("rejected file was modified", err)
	}
}
