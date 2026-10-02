package automationclient

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

func inputSchema(kind string) any {
	if schema := recoverySchema(kind); schema != nil {
		return schema
	}
	text := func() map[string]any { return map[string]any{"type": "string"} }
	props := map[string]any{}
	required := []string{}
	switch kind {
	case "DeploymentStart":
		props["commitSha"] = text()
		props["review"] = map[string]any{"type": "object", "properties": map[string]any{"expectedAppName": text(), "projectId": text(), "appSpecDigest": text(), "bindingsDigest": text(), "serviceRevisions": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "integer"}}}, "required": []string{"expectedAppName", "projectId", "appSpecDigest", "bindingsDigest", "serviceRevisions"}, "additionalProperties": false}
		required = []string{"commitSha", "review"}
	case "InfrastructureAcceptance":
		for _, key := range []string{"reviewId", "digest", "confirmName"} {
			props[key] = text()
		}
		required = []string{"digest", "confirmName"}
	case "ProviderOptionsInput":
		props["kind"] = map[string]any{"type": "string", "enum": []string{"regions", "sizes", "images", "networks"}}
		props["config"] = map[string]any{"type": "object"}
		required = []string{"kind"}
	case "TemporaryEnvironmentInput":
		for _, key := range []string{"projectId", "templateId", "serverId", "name", "sourceSha"} {
			props[key] = text()
		}
		props["name"] = map[string]any{"type": "string", "pattern": environmentName.String()}
		props["sourceSha"] = map[string]any{"type": "string", "pattern": fullCommit.String()}
		props["lifetimeSeconds"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 365 * 24 * 3600}
		required = []string{"projectId", "templateId", "serverId", "name", "sourceSha", "lifetimeSeconds"}
	case "TemporaryEnvironmentExtension":
		props["revision"] = map[string]any{"type": "integer", "minimum": 1}
		props["expiresAt"] = map[string]any{"type": "string", "format": "date-time"}
		required = []string{"revision", "expiresAt"}
	case "TemporaryEnvironmentCleanup":
		props["revision"] = map[string]any{"type": "integer", "minimum": 1}
		props["digest"], props["confirmName"] = text(), text()
		required = []string{"revision", "digest", "confirmName"}
	case "SnapshotReviewInput":
		props["name"] = text()
		props["diskSet"] = map[string]any{"type": "string", "enum": []string{"boot", "all"}}
		props["consistency"] = map[string]any{"type": "string", "enum": []string{"crash-consistent"}}
		props["encryption"] = map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"type": "string", "enum": []string{"provider-managed"}}}, "required": []string{"mode"}, "additionalProperties": false}
		props["retainUntil"] = map[string]any{"type": "string", "format": "date-time"}
		required = []string{"name", "diskSet", "consistency", "encryption"}
	case "ServerCreateReview":
		for _, key := range []string{"projectId", "providerId", "name", "region", "size", "image", "network", "sshKeySecretId", "sourceSnapshotId"} {
			props[key] = text()
		}
		props["config"] = map[string]any{"type": "object"}
		props["bootstrap"] = map[string]any{"type": "object", "properties": map[string]any{
			"method":         map[string]any{"type": "string", "enum": []string{"cloud_init"}},
			"platform":       map[string]any{"type": "string", "enum": []string{"linux-amd64", "linux-arm64"}},
			"imageFamily":    map[string]any{"type": "string", "enum": []string{"ubuntu-24.04", "existing-systemd"}},
			"installRuntime": map[string]any{"type": "boolean"},
		}, "additionalProperties": false, "required": []string{"method", "platform", "imageFamily", "installRuntime"}}

		required = []string{"projectId", "providerId", "name"}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}
func toolDescription(op Operation) map[string]any {
	properties := map[string]any{}
	for _, f := range op.Fields {
		p := map[string]any{"type": "string"}
		switch f {
		case "input":
			properties[f] = inputSchema(op.InputSchema)
			if op.Name == "server_create" || op.Name == "snapshot_accept" || op.Name == "environment_create" {
				properties[f].(map[string]any)["required"] = []string{"reviewId", "digest", "confirmName"}
			}
			if op.InputSchema == "RecoveryConfirmation" {
				action := "delete"
				if op.Name == "backup_restore" {
					action = "restore"
				}
				schema := properties[f].(map[string]any)["properties"].(map[string]any)["confirmation"].(map[string]any)["properties"].(map[string]any)
				schema["action"] = map[string]any{"type": "string", "const": action}
			}
			continue
		case "limit":
			p = map[string]any{"type": "integer", "minimum": 1, "maximum": 500}
		case "after":
			p = map[string]any{"type": "integer", "minimum": 0}
		case "timeoutSeconds":
			p = map[string]any{"type": "integer", "minimum": 1, "maximum": 600}
		case "key":
			p["minLength"] = 8
			p["maxLength"] = 128
		}
		properties[f] = p
	}
	required := op.Required
	if required == nil {
		required = []string{}
	}
	createsReview := op.CreatesReview || op.Name == "server_review" || op.Name == "snapshot_review" || op.Name == "snapshot_delete_review" || op.Name == "environment_review"
	return map[string]any{"name": op.Name, "description": op.Description, "inputSchema": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}, "annotations": map[string]any{"readOnlyHint": !op.Mutation && !createsReview, "destructiveHint": op.Destructive || op.Name == "server_delete" || op.Name == "snapshot_accept" || op.Name == "environment_destroy", "idempotentHint": !createsReview && !op.NonIdempotent && op.Name != "environment_extend", "openWorldHint": true}}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func rpcID(id json.RawMessage) bool {
	var v any
	if json.Unmarshal(id, &v) != nil {
		return false
	}
	switch v.(type) {
	case string, float64:
		return true
	}
	return false
}

// ServeMCP implements bounded stdio tools for protocol 2025-11-25, also accepting
// the compatible 2025-06-18 and 2025-03-26 lifecycles. It never grants approval.
func (c *Client) ServeMCP(parent context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var writeMu, activeMu sync.Mutex
	active := map[string]context.CancelFunc{}
	var workers sync.WaitGroup
	write := func(v any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		if json.NewEncoder(out).Encode(v) != nil {
			cancel()
		}
	}
	reply := func(id json.RawMessage, result any) {
		write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	fail := func(id json.RawMessage, code int, message string) {
		if len(id) == 0 {
			id = json.RawMessage("null")
		}
		write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
	}
	type lineResult struct {
		line []byte
		err  error
		end  bool
	}
	lines := make(chan lineResult, 1)
	if closer, ok := in.(io.Closer); ok {
		defer closer.Close()
	}
	go func() {
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 4096), MaxInputBytes+1)
		for scanner.Scan() {
			item := lineResult{line: append([]byte{}, scanner.Bytes()...)}
			select {
			case lines <- item:
			case <-ctx.Done():
				return
			}
		}
		select {
		case lines <- lineResult{err: scanner.Err(), end: true}:
		case <-ctx.Done():
		}
	}()
	initialized, ready := false, false
	var inputErr error
readLoop:
	for {
		var item lineResult
		select {
		case <-ctx.Done():
			break readLoop
		case item = <-lines:
		}
		if item.end {
			inputErr = item.err
			break
		}
		line := item.line
		var request rpcRequest
		if !utf8.Valid(line) || !json.Valid(line) {
			fail(nil, -32700, "Invalid JSON")
			continue
		}
		if json.Unmarshal(line, &request) != nil || request.JSONRPC != "2.0" || request.Method == "" {
			fail(nil, -32600, "Invalid request")
			continue
		}
		notification := len(request.ID) == 0
		if !notification && !rpcID(request.ID) {
			fail(nil, -32600, "Request ID must be a string or number")
			continue
		}
		if notification {
			switch request.Method {
			case "notifications/initialized":
				if initialized {
					ready = true
				}
			case "notifications/cancelled":
				var params struct {
					RequestID json.RawMessage `json:"requestId"`
				}
				if json.Unmarshal(request.Params, &params) == nil {
					activeMu.Lock()
					stop := active[string(params.RequestID)]
					activeMu.Unlock()
					if stop != nil {
						stop()
					}
				}
			}
			continue
		}
		switch request.Method {
		case "initialize":
			if initialized {
				fail(request.ID, -32600, "Already initialized")
				continue
			}
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			if json.Unmarshal(request.Params, &params) != nil || params.ProtocolVersion == "" {
				fail(request.ID, -32602, "protocolVersion is required")
				continue
			}
			version := params.ProtocolVersion
			if version != "2025-11-25" && version != "2025-06-18" && version != "2025-03-26" {
				version = "2025-11-25"
			}
			initialized = true
			reply(request.ID, map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "dispatch", "version": "1"}, "instructions": "Use a scoped Dispatch service account. Supply reviews, confirmation names and retry keys explicitly. Tool calls never satisfy required human approval. Client cancellation stops waiting only; inspect saved receipts before retrying."})
		case "ping":
			reply(request.ID, map[string]any{})
		case "tools/list":
			if !ready {
				fail(request.ID, -32000, "Initialize before listing tools")
				continue
			}
			var params struct {
				Cursor string `json:"cursor"`
			}
			if len(request.Params) > 0 && json.Unmarshal(request.Params, &params) != nil || params.Cursor != "" {
				fail(request.ID, -32602, "Invalid tool cursor")
				continue
			}
			list := []map[string]any{}
			for _, op := range Operations {
				list = append(list, toolDescription(op))
			}
			reply(request.ID, map[string]any{"tools": list})
		case "tools/call":
			if !ready {
				fail(request.ID, -32000, "Initialize before invoking tools")
				continue
			}
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(request.Params, &params) != nil {
				fail(request.ID, -32602, "Invalid tool parameters")
				continue
			}
			if _, ok := Find(params.Name); !ok {
				fail(request.ID, -32602, "Unknown tool")
				continue
			}
			if len(params.Arguments) == 0 {
				params.Arguments = json.RawMessage("{}")
			}
			args, err := DecodeArguments(params.Arguments)
			if err != nil {
				fail(request.ID, -32602, "Invalid typed arguments")
				continue
			}
			id := string(request.ID)
			activeMu.Lock()
			if _, exists := active[id]; exists || len(active) >= 8 {
				activeMu.Unlock()
				fail(request.ID, -32000, "Request ID is active or tool concurrency limit reached")
				continue
			}
			callCtx, stop := context.WithCancel(ctx)
			active[id] = stop
			activeMu.Unlock()
			workers.Add(1)
			go func(request rpcRequest, name string, args Arguments) {
				defer workers.Done()
				defer stop()
				defer func() { activeMu.Lock(); delete(active, string(request.ID)); activeMu.Unlock() }()
				result := c.Call(callCtx, name, args)
				b, _ := json.Marshal(result)
				reply(request.ID, map[string]any{"content": []map[string]string{{"type": "text", "text": string(b)}}, "structuredContent": result, "isError": !result.OK})
			}(request, params.Name, args)
		default:
			if !strings.HasPrefix(request.Method, "notifications/") {
				fail(request.ID, -32601, "Method not found")
			}
		}
	}
	cancel()
	workers.Wait()
	return inputErr
}
