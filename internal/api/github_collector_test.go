package api

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGitHubCollectorDrainsLogsAndDisablesWorkflowCommands(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python 3 is unavailable")
	}
	raw, err := os.ReadFile("../../examples/github/doodship-logs.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	script := strings.TrimSuffix(strings.TrimPrefix(workflow.Jobs["logs"].Steps[0].Run, "python3 -u - <<'PY'\n"), "PY\n")
	harness := `
import io, json, os, sys, time, urllib.parse, urllib.request
os.environ.update(DOODSHIP_URL="https://instance.test", REPORT_ID="report",
                  ACTIONS_ID_TOKEN_REQUEST_URL="https://identity.test/token?x=1",
                  ACTIONS_ID_TOKEN_REQUEST_TOKEN="private-oidc-credential")
state = os.environ["RESULT_STATE"]
indexes = 0
class FakeHTTP:
    def open(self, request, timeout=30):
        global indexes
        parsed = urllib.parse.urlsplit(request.full_url)
        if parsed.hostname == "identity.test":
            assert urllib.parse.parse_qs(parsed.query)["audience"] == ["doodship:check:report"]
            return io.BytesIO(json.dumps({"value": "signed-workflow-token"}).encode())
        assert request.get_header("Authorization") == "Bearer signed-workflow-token"
        query = urllib.parse.parse_qs(parsed.query)
        if "stream" not in query:
            indexes += 1
            result = {"streams": [{"id": "job-build", "name": "build", "state": state}],
                      "complete": indexes > 1, "state": state}
        else:
            offset = int(query["offset"][0])
            text = "first\n::error::untrusted-build-output\nlast\n"
            end = min(offset + 8, len(text))
            result = {"text": text[offset:end], "next": end,
                      "complete": indexes > 1 and end == len(text),
                      "incomplete": os.environ["INCOMPLETE"] == "true"}
        return io.BytesIO(json.dumps(result).encode())
urllib.request.build_opener = lambda *args: FakeHTTP()
time.sleep = lambda delay: None
exec(compile(sys.stdin.read(), "doodship-logs.yml", "exec"))
`
	for _, test := range []struct {
		state, incomplete string
		exit              int
	}{{"succeeded", "false", 0}, {"failed", "false", 1}, {"succeeded", "true", 1}} {
		command := exec.Command("python3", "-c", harness)
		command.Stdin = strings.NewReader(script)
		command.Env = append(os.Environ(), "RESULT_STATE="+test.state, "INCOMPLETE="+test.incomplete)
		output, err := command.CombinedOutput()
		exit := 0
		if err != nil {
			var ok bool
			var status *exec.ExitError
			status, ok = err.(*exec.ExitError)
			if !ok {
				t.Fatal(err)
			}
			exit = status.ExitCode()
		}
		if exit != test.exit || !bytes.Contains(output, []byte("first\n::error::untrusted-build-output\nlast\n")) {
			t.Fatalf("collector lost logs or outcome: %d %s", exit, output)
		}
		if bytes.Index(output, []byte("::stop-commands::")) > bytes.Index(output, []byte("untrusted-build-output")) || bytes.Contains(output, []byte("signed-workflow-token")) || bytes.Contains(output, []byte("private-oidc-credential")) {
			t.Fatal("workflow command or credential escaped")
		}
	}
}
