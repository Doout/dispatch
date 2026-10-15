package api

import (
	"encoding/json"
	"net/http"
)

func writeGitBranchFixture(w http.ResponseWriter, branch, sha string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"ref": "refs/heads/" + branch, "object": map[string]string{"type": "commit", "sha": sha}})
}
