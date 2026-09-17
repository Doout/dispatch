package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/githubapp"
)

// A malformed file retains its previous resources. Other files can still sync.
// Conflicting identities invalidate both files so ordering never picks a winner.
func (s *Service) parsePartialConfiguration(ctx context.Context, source core.ConfigSource, head string, files []githubapp.RepositoryFile) ([]parsedResource, map[string]string) {
	var parsed []parsedResource
	failures := map[string]string{}
	for _, file := range files {
		resources, err := s.parseConfiguration(ctx, source, head, []githubapp.RepositoryFile{file})
		if err != nil {
			failures[file.Path] = err.Error()
			continue
		}
		if len(parsed)+len(resources) > 256 {
			failures[file.Path] = "configuration expands to more than 256 resources"
			continue
		}
		parsed = append(parsed, resources...)
	}
	seen := map[string]string{}
	for _, resource := range parsed {
		key := resource.document.Kind + "/" + resource.document.Metadata.Name
		if previous, ok := seen[key]; ok {
			message := fmt.Sprintf("%s and %s both define %s", previous, resource.path, key)
			failures[previous], failures[resource.path] = message, message
		} else {
			seen[key] = resource.path
		}
	}
	valid := parsed[:0]
	for _, resource := range parsed {
		if failures[resource.path] == "" {
			valid = append(valid, resource)
		}
	}
	return valid, failures
}

func partialSyncMessage(messages []string) string {
	sort.Strings(messages)
	return "Some applications could not sync. Unaffected applications continue.\n" + strings.Join(messages, "\n")
}
