package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/doout/dispatch/internal/automationclient"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
func run(ctx context.Context, args []string, in io.Reader, out, diagnostics io.Writer) int {
	emit := func(r automationclient.Result) int { _ = json.NewEncoder(out).Encode(r); return r.ExitCode() }
	global := flag.NewFlagSet("dispatchctl", flag.ContinueOnError)
	global.SetOutput(diagnostics)
	endpoint := global.String("url", os.Getenv("DISPATCH_URL"), "Controller origin, normally HTTPS")
	tokenFile := global.String("token-file", os.Getenv("DISPATCH_TOKEN_FILE"), "Mode 0600 file containing a scoped service-account token")
	requestTimeout := global.Duration("request-timeout", 30*time.Second, "Timeout for each API request, maximum 10m")
	global.Usage = func() {
		fmt.Fprintln(diagnostics, "Usage: dispatchctl [--url URL --token-file FILE] <resource> <action> [flags]\n       dispatchctl [global flags] mcp\nAll commands return dispatch.client/v1 JSON. No owner token is needed.\nCommands:")
		for _, op := range automationclient.Operations {
			fmt.Fprintln(diagnostics, "  "+strings.ReplaceAll(op.Name, "_", " ")+"  "+op.Description)
		}
		global.PrintDefaults()
	}
	if err := global.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return emit(automationclient.Failure("invalid_input", "Invalid global flags"))
	}
	rest := global.Args()
	if len(rest) == 0 {
		global.Usage()
		return 2
	}
	configurationFailure := func(message string) int {
		if rest[0] == "mcp" {
			fmt.Fprintln(diagnostics, message)
			return 2
		}
		return emit(automationclient.Failure("configuration", message))
	}
	token, err := automationclient.ReadToken(*tokenFile)
	if err != nil {
		return configurationFailure(err.Error())
	}
	client, err := automationclient.New(*endpoint, token, *requestTimeout)
	if err != nil {
		return configurationFailure(err.Error())
	}
	if rest[0] == "mcp" {
		if len(rest) != 1 {
			fmt.Fprintln(diagnostics, "mcp does not accept operation arguments")
			return 2
		}
		if err := client.ServeMCP(ctx, in, out); err != nil {
			fmt.Fprintln(diagnostics, "MCP input exceeded the supported message size or could not be read")
			return 1
		}
		return 0
	}
	name, used := rest[0], 1
	// Match the longest registered command spelling, including three-word
	// review and operation commands. Underscore spellings remain valid.
	for count := 1; count <= len(rest) && !strings.HasPrefix(rest[count-1], "-"); count++ {
		candidate := strings.Join(rest[:count], "_")
		if _, ok := automationclient.Find(candidate); ok {
			name, used = candidate, count
		}
	}
	if _, ok := automationclient.Find(name); !ok {
		return emit(automationclient.Failure("invalid_input", "Unknown command; use --help for supported operations"))
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	var a automationclient.Arguments
	flags.StringVar(&a.TemplateID, "template", "", "Approved service template ID")
	flags.StringVar(&a.PolicyID, "policy", "", "Scheduled workload backup policy ID")
	flags.StringVar(&a.RunID, "run", "", "Owned service provision run ID")
	flags.StringVar(&a.BackupID, "backup", "", "Native workload backup ID")
	flags.StringVar(&a.OperationID, "operation", "", "Original infrastructure or workload backup operation ID")
	flags.StringVar(&a.DestinationID, "destination", "", "Reviewed destination service provision run ID")
	flags.StringVar(&a.ReviewID, "review", "", "Original runtime retention review ID")
	flags.StringVar(&a.ProjectID, "project", "", "Project ID")
	flags.StringVar(&a.ProviderID, "provider", "", "Assigned provider ID")
	flags.StringVar(&a.SnapshotID, "snapshot", "", "Owned machine snapshot ID")
	flags.StringVar(&a.EnvironmentID, "environment", "", "Temporary environment ID")
	flags.StringVar(&a.AppID, "app", "", "Application ID")
	flags.StringVar(&a.DeploymentID, "deployment", "", "Deployment ID")
	flags.StringVar(&a.ReceiptID, "receipt", "", "Mutation receipt ID")
	flags.StringVar(&a.ServerID, "server", "", "Managed server ID")
	flags.StringVar(&a.Revision, "revision", "", "Commit or source revision to preview")
	flags.StringVar(&a.Key, "key", "", "Caller-chosen idempotency key, preserve for retries")
	flags.IntVar(&a.Limit, "limit", 0, "Log entries, 1 to 500")
	flags.Int64Var(&a.After, "after", 0, "Saved log cursor")
	flags.IntVar(&a.TimeoutSeconds, "timeout", 0, "Wait timeout in seconds, 1 to 600")
	input := flags.String("input", "", "Typed JSON request file, or - for stdin")
	flags.Bool("json", true, "Emit stable JSON, always enabled")
	if err := flags.Parse(rest[used:]); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return emit(automationclient.Failure("invalid_input", "Invalid command flags"))
	}
	if flags.NArg() != 0 {
		return emit(automationclient.Failure("invalid_input", "Unexpected positional arguments"))
	}
	if *input != "" {
		var reader io.Reader = in
		if *input != "-" {
			f, e := os.Open(*input)
			if e != nil {
				return emit(automationclient.Failure("invalid_input", "Cannot open input file"))
			}
			defer f.Close()
			reader = f
		}
		b, e := io.ReadAll(io.LimitReader(reader, automationclient.MaxInputBytes+1))
		if e != nil || len(b) > automationclient.MaxInputBytes || !json.Valid(b) {
			return emit(automationclient.Failure("invalid_input", "Input must be one JSON document within 1 MiB"))
		}
		a.Input = b
	}
	return emit(client.Call(ctx, name, a))
}
