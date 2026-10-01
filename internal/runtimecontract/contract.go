// Package runtimecontract defines the versioned behavior shared by runtime
// executors. Capabilities describe implemented operations, not planned support.
package runtimecontract

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

const APIVersion = "dispatch.runtime/v1"

type Operation string

const (
	Deploy   Operation = "deploy"
	Inspect  Operation = "inspect"
	Logs     Operation = "logs"
	Start    Operation = "start"
	Stop     Operation = "stop"
	Rollback Operation = "rollback"
	Destroy  Operation = "destroy"
)

func Operations() []Operation {
	return []Operation{Deploy, Inspect, Logs, Start, Stop, Rollback, Destroy}
}

type Code string

const (
	InvalidRequest    Code = "invalid_request"
	Unsupported       Code = "unsupported_operation"
	OwnershipConflict Code = "ownership_conflict"
	Conflict          Code = "operation_conflict"
	Cancelled         Code = "cancelled"
	DeadlineExceeded  Code = "deadline_exceeded"
	Unavailable       Code = "runtime_unavailable"
	Uncertain         Code = "outcome_unknown"
	Failed            Code = "execution_failed"
)

// Error contains a safe public description. A cause is retained for errors.Is
// without exposing runtime command output or credentials through serialization.
type Error struct {
	Code    Code      `json:"code"`
	Action  Operation `json:"operation,omitempty"`
	Message string    `json:"message"`
	Cause   error     `json:"-"`
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

type Capability struct {
	Operation Operation `json:"operation"`
	Supported bool      `json:"supported"`
	Reason    string    `json:"reason,omitempty"`
}

type Manifest struct {
	APIVersion   string       `json:"apiVersion"`
	Driver       string       `json:"driver"`
	TargetMode   string       `json:"targetMode"`
	Capabilities []Capability `json:"capabilities"`
}

func Describe(driver, mode string, supported ...Operation) Manifest {
	m := Manifest{APIVersion: APIVersion, Driver: driver, TargetMode: mode, Capabilities: []Capability{}}
	for _, op := range Operations() {
		c := Capability{Operation: op, Supported: slices.Contains(supported, op)}
		if !c.Supported {
			c.Reason = "This driver does not implement this operation through the runtime contract."
		}
		m.Capabilities = append(m.Capabilities, c)
	}
	return m
}

// Check must run before credentials are materialized or runtime resources change.
func (m Manifest) Check(ctx context.Context, op Operation) error {
	if err := ctx.Err(); err != nil {
		return Classify(op, err)
	}
	if m.APIVersion != APIVersion {
		return &Error{Code: Unsupported, Action: op, Message: "The runtime contract version is not supported."}
	}
	for _, c := range m.Capabilities {
		if c.Operation == op && c.Supported {
			return nil
		}
	}
	return &Error{Code: Unsupported, Action: op, Message: fmt.Sprintf("Runtime %s does not support %s on this target.", m.Driver, op)}
}

func Classify(op Operation, err error) error {
	if err == nil {
		return nil
	}
	var runtimeError *Error
	if errors.As(err, &runtimeError) {
		return err
	}
	code, message := Failed, "The runtime operation failed; inspect its recorded diagnostic evidence."
	if errors.Is(err, context.Canceled) {
		code, message = Cancelled, "The runtime operation was cancelled; inspect resources before retrying."
	} else if errors.Is(err, context.DeadlineExceeded) {
		code, message = DeadlineExceeded, "The runtime deadline expired; inspect resources before retrying."
	}
	return &Error{Code: code, Action: op, Message: message, Cause: err}
}
