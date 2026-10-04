package provider

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

func registerServerActionRoutes(mux *http.ServeMux, adapter Provider, respond func(http.ResponseWriter, int, any, error)) {
	mux.HandleFunc("POST /v1/servers/{id}/power", func(w http.ResponseWriter, r *http.Request) {
		p, ok := adapter.(PowerProvider)
		if !ok {
			writeProblem(w, NewProblem(422, "Power controls unsupported", "This provider has no machine power implementation."))
			return
		}
		key, ok := mutationKey(w, r)
		if !ok || !pathID(w, r) {
			return
		}
		var in PowerServerRequest
		if !decodeRequest(w, r, &in) {
			return
		}
		if ValidatePowerRequest(in) != nil {
			writeProblem(w, NewProblem(422, "Power request invalid", "Supply a supported action and machine identity digest."))
			return
		}
		op, err := p.PowerServer(r.Context(), key, r.PathValue("id"), in)
		respond(w, 202, op, err)
	})
	mux.HandleFunc("POST /v1/servers/{id}/promote", func(w http.ResponseWriter, r *http.Request) {
		p, ok := adapter.(PromotionProvider)
		if !ok {
			writeProblem(w, NewProblem(422, "Clone promotion unsupported", "This provider has no clone promotion implementation."))
			return
		}
		key, ok := mutationKey(w, r)
		if !ok || !pathID(w, r) {
			return
		}
		var in PromoteServerRequest
		if !decodeRequest(w, r, &in) {
			return
		}
		if ValidatePromotionRequest(in) != nil {
			writeProblem(w, NewProblem(422, "Promotion request invalid", "Supply an explicit destination network and clone identity digest."))
			return
		}
		op, err := p.PromoteServer(r.Context(), key, r.PathValue("id"), in)
		respond(w, 202, op, err)
	})
}
func (c *Client) PowerServer(ctx context.Context, key, id string, in PowerServerRequest) (Operation, error) {
	if !ValidID(key) || !ValidID(id) || ValidatePowerRequest(in) != nil {
		return Operation{}, errors.New("power mutation requires valid identities and an action")
	}
	return c.operationRequest(ctx, "POST", "/v1/servers/"+url.PathEscape(id)+"/power", key, in, 202)
}
func (c *Client) PromoteServer(ctx context.Context, key, id string, in PromoteServerRequest) (Operation, error) {
	if !ValidID(key) || !ValidID(id) || ValidatePromotionRequest(in) != nil {
		return Operation{}, errors.New("promotion mutation requires valid identities and a network")
	}
	return c.operationRequest(ctx, "POST", "/v1/servers/"+url.PathEscape(id)+"/promote", key, in, 202)
}
