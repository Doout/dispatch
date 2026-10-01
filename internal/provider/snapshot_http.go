package provider

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

type snapshotsResponse struct {
	Items []Snapshot `json:"items"`
}

func registerSnapshotRoutes(mux *http.ServeMux, adapter Provider, respond func(http.ResponseWriter, int, any, error)) {
	capable := func(w http.ResponseWriter) (SnapshotProvider, bool) {
		snapshot, ok := adapter.(SnapshotProvider)
		if !ok {
			writeProblem(w, NewProblem(422, "Snapshots unsupported", "This provider has no snapshot implementation."))
		}
		return snapshot, ok
	}
	mux.HandleFunc("POST /v1/snapshots", func(w http.ResponseWriter, r *http.Request) {
		p, ok := capable(w)
		if !ok {
			return
		}
		key, ok := mutationKey(w, r)
		if !ok {
			return
		}
		var in CreateSnapshotRequest
		if !decodeRequest(w, r, &in) {
			return
		}
		op, err := p.CreateSnapshot(r.Context(), key, in)
		respond(w, 202, op, err)
	})
	mux.HandleFunc("GET /v1/servers/{id}/snapshots", func(w http.ResponseWriter, r *http.Request) {
		p, ok := capable(w)
		if !ok || !pathID(w, r) {
			return
		}
		items, err := p.Snapshots(r.Context(), r.PathValue("id"))
		if items == nil {
			items = []Snapshot{}
		}
		respond(w, 200, snapshotsResponse{Items: items}, err)
	})
	mux.HandleFunc("GET /v1/snapshots/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := capable(w)
		if !ok || !pathID(w, r) {
			return
		}
		item, err := p.Snapshot(r.Context(), r.PathValue("id"))
		respond(w, 200, item, err)
	})
	mux.HandleFunc("DELETE /v1/snapshots/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := capable(w)
		if !ok || !pathID(w, r) {
			return
		}
		key, ok := mutationKey(w, r)
		if !ok {
			return
		}
		op, err := p.DeleteSnapshot(r.Context(), key, r.PathValue("id"))
		respond(w, 202, op, err)
	})
	mux.HandleFunc("POST /v1/snapshots/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		p, ok := capable(w)
		if !ok || !pathID(w, r) {
			return
		}
		key, ok := mutationKey(w, r)
		if !ok {
			return
		}
		var in RestoreServerRequest
		if !decodeRequest(w, r, &in) {
			return
		}
		op, err := p.RestoreServer(r.Context(), key, r.PathValue("id"), in)
		respond(w, 202, op, err)
	})
}
func (c *Client) CreateSnapshot(ctx context.Context, key string, in CreateSnapshotRequest) (Operation, error) {
	if !ValidID(key) || !ValidID(in.SourceServerID) {
		return Operation{}, errors.New("snapshot mutation identity is invalid")
	}
	return c.operationRequest(ctx, "POST", "/v1/snapshots", key, in, 202)
}
func (c *Client) Snapshots(ctx context.Context, server string) ([]Snapshot, error) {
	if !ValidID(server) {
		return nil, errors.New("snapshot source identity is invalid")
	}
	var out snapshotsResponse
	err := c.request(ctx, "GET", "/v1/servers/"+url.PathEscape(server)+"/snapshots", "", nil, &out, 200)
	if err != nil {
		return nil, err
	}
	if out.Items == nil {
		return nil, errors.New("snapshot list is absent")
	}
	seen := map[string]bool{}
	for _, s := range out.Items {
		if s.SourceServerID != server || seen[s.ID] || ValidateSnapshot(s) != nil {
			return nil, errors.New("snapshot list contains inconsistent identity evidence")
		}
		seen[s.ID] = true
	}
	return out.Items, nil
}
func (c *Client) Snapshot(ctx context.Context, id string) (Snapshot, error) {
	var out Snapshot
	if !ValidID(id) {
		return out, errors.New("snapshot identity is invalid")
	}
	err := c.request(ctx, "GET", "/v1/snapshots/"+url.PathEscape(id), "", nil, &out, 200)
	if err == nil {
		if out.ID != id {
			return out, errors.New("provider returned a different snapshot identity")
		}
		err = ValidateSnapshot(out)
	}
	return out, err
}
func (c *Client) DeleteSnapshot(ctx context.Context, key, id string) (Operation, error) {
	if !ValidID(key) || !ValidID(id) {
		return Operation{}, errors.New("snapshot deletion identity is invalid")
	}
	return c.operationRequest(ctx, "DELETE", "/v1/snapshots/"+url.PathEscape(id), key, nil, 202)
}
func (c *Client) RestoreServer(ctx context.Context, key, id string, in RestoreServerRequest) (Operation, error) {
	if !ValidID(key) || !ValidID(id) || !in.Policy.SafeClone() {
		return Operation{}, errors.New("restore requires valid identities and an isolated fresh-identity policy")
	}
	return c.operationRequest(ctx, "POST", "/v1/snapshots/"+url.PathEscape(id)+"/restore", key, in, 202)
}
