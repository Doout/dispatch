package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/doout/dispatch/internal/tenancy"
)

// createUser is an offline operator action for installations without email
// delivery. The operator verifies the user's identity before running it.
func createUser(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("create-user", flag.ContinueOnError)
	flags.SetOutput(out)
	email := flags.String("email", "", "verified user email")
	name := flags.String("name", "", "user name")
	passwordFile := flags.String("password-file", "", "private file containing the initial password")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *email == "" || *name == "" || *passwordFile == "" {
		return errors.New("create-user requires --email, --name and --password-file; stop the hosted controller first")
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
	if err = catalog.ProvisionUser(ctx, *email, *name, password); err != nil {
		if errors.Is(err, tenancy.ErrConflict) {
			return errors.New("an account already exists for that email")
		}
		if errors.Is(err, tenancy.ErrDenied) {
			return errors.New("bootstrap the initial platform administrator before creating users")
		}
		return errors.New("cannot create user")
	}
	_, err = fmt.Fprintln(out, "Verified user created. No platform role or tenant membership was granted.")
	return err
}
