package installation

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

var Version = "development"

const help = `Usage: dispatch <command> [options]

  serve                  Run the controller (also the default with no arguments)
  install                Install a Docker controller
  upgrade                Pull and apply a controller upgrade
  status                 Show the managed installation and its health
  recover                Recover an interrupted install or upgrade
  auto-upgrade enable    Enable a Docker updater container
  auto-upgrade disable   Disable automatic upgrades
  auto-upgrade status    Show the updater container status
  version                Show the CLI version

Installation commands default to --dir /opt/dispatch.
Run dispatch <command> --help for options.
`

// Run returns false for server invocations so existing container entrypoints
// and no-argument controller commands remain compatible.
func Run(ctx context.Context, args []string, out io.Writer) (bool, error) {
	if len(args) == 0 || args[0] == "serve" {
		if len(args) > 1 {
			return true, errors.New("serve takes configuration from environment variables")
		}
		return false, nil
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(out, help)
		return true, nil
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintln(out, "Dispatch", Version)
		return true, nil
	}
	command := args[0]
	rest := args[1:]
	action := ""
	if command == "auto-upgrade" {
		if len(rest) == 0 {
			return true, errors.New("use auto-upgrade enable, disable, or status")
		}
		action, rest = rest[0], rest[1:]
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "/opt/dispatch", "managed installation directory")
	timeout := fs.Duration("timeout", 90*time.Second, "time to wait for the controller to become healthy")
	var image *string
	var pull *bool
	var check *bool
	var c Config
	var envFile *string
	var interval *time.Duration
	var registryConfig *string
	switch command {
	case "install":
		image = fs.String("image", DefaultImage, "controller image to install and follow for upgrades")
		pull = fs.Bool("pull", true, "pull the image before installing (false uses a local image)")
		fs.StringVar(&c.Name, "name", "dispatch", "Compose project name")
		fs.StringVar(&c.Bind, "bind", "127.0.0.1", "host IP address to listen on")
		fs.IntVar(&c.Port, "port", 8080, "host port")
		fs.StringVar(&c.Volume, "data-volume", "", "new data volume (defaults to NAME-data)")
		fs.StringVar(&c.Socket, "docker-socket", "/var/run/docker.sock", "local Docker socket")
		fs.StringVar(&c.PublicURL, "public-url", "", "public controller URL")
		fs.StringVar(&c.ProxyNetwork, "proxy-network", "", "existing Traefik Docker network (optional)")
		fs.StringVar(&c.Hostname, "hostname", "", "hostname for the Traefik route")
		fs.StringVar(&c.TLSResolver, "tls-resolver", "", "Traefik certificate resolver (optional)")
		envFile = fs.String("env-file", "", "file of literal KEY=value controller settings to copy")
	case "upgrade":
		image = fs.String("image", "", "change the image reference followed by this installation")
		pull = fs.Bool("pull", true, "pull before upgrading (false uses a local image)")
		check = fs.Bool("check", false, "check for a new image without restarting the controller")
	case "auto-upgrade":
		interval = fs.Duration("interval", 24*time.Hour, "time between update checks")
		pull = fs.Bool("pull", true, "pull the channel image before each check")
		registryConfig = fs.String("registry-config", "", "Docker config directory for private registry credentials (optional)")
	case "status", "recover":
	default:
		return true, fmt.Errorf("unknown command %q; run dispatch --help", command)
	}
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return true, nil
		}
		return true, err
	}
	if fs.NArg() != 0 {
		return true, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if *timeout < time.Second || *timeout > 10*time.Minute {
		return true, errors.New("timeout must be between 1 second and 10 minutes")
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return true, err
	}
	if strings.ContainsAny(abs, ":\n\r$%") {
		return true, errors.New("installation directory contains unsupported characters")
	}
	m := New(abs)
	m.Out = out
	m.Timeout = *timeout
	if command == "auto-upgrade" {
		if *interval < time.Minute || *interval > 30*24*time.Hour {
			return true, errors.New("interval must be between 1 minute and 30 days")
		}
		if action == "run" {
			return true, m.RunAutomatic(ctx, *interval, *pull)
		}
	}
	return true, m.Locked(func() error {
		switch command {
		case "install":
			c.Image = *image
			if c.Volume == "" {
				c.Volume = c.Name + "-data"
			}
			return m.Install(ctx, c, *envFile, *pull)
		case "upgrade":
			return m.Upgrade(ctx, *image, *pull, *check)
		case "status":
			return m.Status(ctx)
		case "recover":
			return m.Recover(ctx)
		case "auto-upgrade":
			return m.AutoUpgrade(ctx, action, *interval, *pull, *registryConfig)
		}
		return nil
	})
}
