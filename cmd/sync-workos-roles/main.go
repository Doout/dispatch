package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	catalog "github.com/doout/dispatch/internal/tenancy/roles"
	workos "github.com/workos/workos-go/v10"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("sync-workos-roles", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	keyPath := flags.String("api-key-file", os.Getenv("DISPATCH_WORKOS_API_KEY_FILE"), "Private file containing the target environment's WorkOS API key")
	apply := flags.Bool("apply", false, "Apply the displayed changes; otherwise only preview them")
	dryRun := flags.Bool("dry-run", false, "Preview changes without writing to WorkOS, the default")
	definitions := flags.Bool("print-definitions", false, "Print the hosted role catalog as JSON without contacting WorkOS")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || (*apply && *dryRun) || (*definitions && (*apply || *dryRun)) {
		return errors.New("use --apply, --dry-run or --print-definitions without positional arguments")
	}
	roles, permissions := catalog.Definitions(), catalog.Permissions()
	if err := validateCatalog(roles, permissions); err != nil {
		return err
	}
	if *definitions {
		return json.NewEncoder(out).Encode(struct {
			Roles       []catalog.Definition `json:"roles"`
			Permissions []catalog.Permission `json:"permissions"`
		}{roles, permissions})
	}
	key, err := readKey(*keyPath)
	if err != nil {
		return err
	}
	client := workos.NewClient(key, workos.WithHTTPClient(&http.Client{Timeout: 20 * time.Second}), workos.WithMaxRetries(0))
	return syncRoles(ctx, client, roles, permissions, *apply, out)
}

func syncRoles(ctx context.Context, client *workos.Client, roles []catalog.Definition, permissions []catalog.Permission, apply bool, out io.Writer) error {
	plan, err := buildPlan(ctx, client, roles, permissions)
	if err != nil {
		return err
	}
	if len(plan) == 0 {
		_, err = fmt.Fprintln(out, "WorkOS roles and permissions are up to date.")
		return err
	}
	for _, change := range plan {
		if _, err := fmt.Fprintln(out, change.summary()); err != nil {
			return err
		}
	}
	if !apply {
		_, err = fmt.Fprintln(out, "Dry run. Use --apply to update this WorkOS environment.")
		return err
	}
	for _, change := range plan {
		if err := change.apply(ctx, client); err != nil {
			return apiError(change.summary(), err)
		}
	}
	_, err = fmt.Fprintf(out, "Applied %d changes.\n", len(plan))
	return err
}

func readKey(path string) (string, error) {
	if path == "" {
		return "", errors.New("set --api-key-file or DISPATCH_WORKOS_API_KEY_FILE")
	}
	link, err := os.Lstat(path)
	if err != nil || !link.Mode().IsRegular() {
		return "", errors.New("WorkOS API key must be in a private regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot open WorkOS API key file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(link, info) || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("WorkOS API key must be in a private regular file, mode 0600 or 0400")
	}
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	key := strings.TrimSpace(string(raw))
	if err != nil || len(raw) > 4096 || key == "" || strings.ContainsAny(key, "\r\n") {
		return "", errors.New("WorkOS API key file is empty or invalid")
	}
	return key, nil
}

// Never print API response bodies or transport errors, which may contain
// sensitive request details. HTTP status and operation identify failed steps.
func apiError(operation string, err error) error {
	var api *workos.APIError
	if errors.As(err, &api) {
		return fmt.Errorf("%s: WorkOS returned HTTP %d", operation, api.StatusCode)
	}
	return fmt.Errorf("%s: WorkOS request failed", operation)
}
