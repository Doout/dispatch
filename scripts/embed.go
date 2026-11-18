// Package scripts provides the installer shared by the bootstrap and CLI update commands.
package scripts

import _ "embed"

// InstallCLI installs a verified doodship release archive.
//
//go:embed install-cli.sh
var InstallCLI string
