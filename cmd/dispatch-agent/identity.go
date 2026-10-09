package main

import (
	"github.com/doout/dispatch/internal/edgeclient"
	"strings"
)

type edgeIdentity = edgeclient.Identity
type edgeSession = edgeclient.Session
type edgeChallenge = edgeclient.Challenge

var loadIdentity = edgeclient.LoadIdentity
var obtainSession = edgeclient.ObtainSession

func validEdgeMethod(method string) bool {
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return method == strings.ToUpper(method)
	default:
		return false
	}
}
