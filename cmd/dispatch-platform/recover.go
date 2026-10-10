package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/doout/dispatch/internal/tenancy"
)

func recoverAdmin(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("recover-admin", flag.ContinueOnError)
	flags.SetOutput(out)
	email := flags.String("email", "", "existing platform administrator email")
	passwordFile := flags.String("password-file", "", "private file containing the new password")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *email == "" || *passwordFile == "" {
		return errors.New("recover-admin requires --email and --password-file; stop the hosted controller first")
	}
	password, err := privateFile(*passwordFile)
	if err != nil {
		return err
	}
	if len(password) < 12 || len(password) > 72 {
		return errors.New("password must contain 12 to 72 bytes")
	}
	root, database, err := catalogConfiguration()
	if err != nil {
		return err
	}
	unlock, err := lockDirectory(root)
	if err != nil {
		return err
	}
	defer unlock()
	catalog, err := tenancy.Open(ctx, database)
	if err != nil {
		return errors.New("cannot open hosted catalog")
	}
	defer catalog.Close()
	release, err := catalog.AcquireControllerLease(ctx, nil)
	if err != nil {
		return err
	}
	defer release()
	if err = catalog.Migrate(ctx); err != nil {
		return errors.New("cannot migrate hosted catalog")
	}
	if err = catalog.RecoverPlatformAdminPassword(ctx, *email, password); err != nil {
		if errors.Is(err, tenancy.ErrDenied) {
			return errors.New("no active, verified platform administrator matches that email")
		}
		return errors.New("cannot recover platform administrator")
	}
	_, err = fmt.Fprintln(out, "Administrator password changed. Existing sessions and sign-in handoffs are revoked. Tenant memberships are unchanged.")
	return err
}
