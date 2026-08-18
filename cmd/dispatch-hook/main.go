package main

import (
	"fmt"
	"os"

	"github.com/doout/dispatch/internal/hookresult"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dispatch-hook:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) != 4 || arguments[1] != "set" {
		return fmt.Errorf("usage: dispatch-hook output set <name> <value> | dispatch-hook helm set <path> <value>")
	}
	path := os.Getenv("DISPATCH_RESULT_FILE")
	switch arguments[0] {
	case "output":
		return hookresult.SetOutput(path, arguments[2], arguments[3])
	case "helm":
		return hookresult.SetHelmValue(path, arguments[2], arguments[3])
	default:
		return fmt.Errorf("unknown result section %q", arguments[0])
	}
}
