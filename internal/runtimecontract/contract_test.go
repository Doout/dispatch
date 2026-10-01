package runtimecontract

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestContractRejectsUnknownVersionAndOperation(t *testing.T) {
	m := Describe("docker", "local", Deploy)
	for _, op := range []Operation{Rollback, "exec", ""} {
		var failure *Error
		if err := m.Check(context.Background(), op); !errors.As(err, &failure) || failure.Code != Unsupported {
			t.Fatalf("accepted unsupported operation %q: %v", op, err)
		}
	}
	m.APIVersion = "dispatch.runtime/v2"
	if err := m.Check(context.Background(), Deploy); err == nil {
		t.Fatal("accepted an unknown contract version")
	}
}

func TestClassifiedErrorsPreserveCauseWithoutSerializingIt(t *testing.T) {
	for _, test := range []struct {
		cause error
		code  Code
	}{
		{context.Canceled, Cancelled}, {context.DeadlineExceeded, DeadlineExceeded}, {errors.New("secret-value"), Failed},
	} {
		err := Classify(Deploy, test.cause)
		var classified *Error
		if !errors.As(err, &classified) || classified.Code != test.code || !errors.Is(err, test.cause) {
			t.Fatalf("wrong classification: %#v", err)
		}
		encoded, marshalErr := json.Marshal(classified)
		if marshalErr != nil || strings.Contains(string(encoded), "secret-value") {
			t.Fatalf("unsafe error: %s: %v", encoded, marshalErr)
		}
	}
}
