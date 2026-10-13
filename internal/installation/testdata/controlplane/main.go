// A disposable TLS control plane for the Docker installation test.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/doout/dispatch/internal/instancebridge"
)

func main() {
	origin := os.Args[1]
	if len(os.Args) > 2 && os.Args[2] == "status" {
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Get(origin + "/fixture/status")
		if err != nil {
			fmt.Fprintln(os.Stderr, "fixture unavailable")
			os.Exit(1)
		}
		defer response.Body.Close()
		io.Copy(os.Stdout, response.Body)
		return
	}
	broker := instancebridge.NewBroker()
	var count atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/fixture/status", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"connected": broker.Connected("instance:1"), "enrollments": count.Load()})
	})
	mux.HandleFunc("/api/v1/instance-connections/enroll", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		if in["enrollmentToken"] != "disposable-once" || in["remoteAccess"] != true || count.Load() > 0 {
			w.WriteHeader(401)
			return
		}
		count.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"instanceID": "instance", "credential": "disposable-credential", "generation": 1, "ownerSubject": "cp-owner", "websocketURL": strings.Replace(origin, "https:", "wss:", 1) + "/api/v1/instance-connections/ws"})
	})
	mux.HandleFunc("/api/v1/instance-connections/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer disposable-credential" {
			w.WriteHeader(401)
			return
		}
		broker.ServeConnection(w, r, "instance:1")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	server := http.Server{Addr: ":8443", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := server.ListenAndServeTLS("/fixtures/test-ca.crt", "/fixtures/test-key.pem"); err != nil {
		fmt.Fprintln(os.Stderr, "fixture stopped")
		os.Exit(1)
	}
}
