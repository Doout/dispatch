package agentruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRuntimeReadinessRequiresDaemonComposeAndGit(t *testing.T) {
	for _, fail := range []int{-1, 0, 1, 2} {
		calls := []string{}
		err := checkPrerequisites(context.Background(), func(_ context.Context, name string, args ...string) error {
			calls = append(calls, name+" "+strings.Join(args, " "))
			if len(calls)-1 == fail {
				return errors.New("private command output")
			}
			return nil
		})
		if (err != nil) != (fail >= 0) {
			t.Fatalf("step %d readiness: %v", fail, err)
		}
		if err != nil && strings.Contains(err.Error(), "private") {
			t.Fatal("runtime command output leaked")
		}
		if fail < 0 && len(calls) != 3 {
			t.Fatal(calls)
		}
	}
}
