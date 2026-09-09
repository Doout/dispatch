package api

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOverviewWatch(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/overview/watch", nil)
	req.Header.Set("Authorization", "Bearer secret")
	baselineReq, _ := http.NewRequest("GET", server.URL+"/api/v1/overview", nil)
	baselineReq.Header.Set("Authorization", "Bearer secret")
	baseline, err := http.DefaultClient.Do(baselineReq)
	if err != nil {
		t.Fatal(err)
	}
	baseline.Body.Close()
	req.Header.Set("Last-Event-ID", baseline.Header.Get("X-Overview-Version"))
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	events := make(chan string, 10)
	go func() {
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 10*1024*1024)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				events <- scanner.Text()
			}
		}
	}()

	select {
	case <-events:
		t.Fatal("duplicate idle snapshot")
	case <-time.After(700 * time.Millisecond):
	}
	post, _ := http.NewRequest("POST", server.URL+"/api/v1/projects", bytes.NewBufferString(`{"name":"Live update"}`))
	post.Header.Set("Authorization", "Bearer secret")
	post.Header.Set("Content-Type", "application/json")
	result, err := http.DefaultClient.Do(post)
	if err != nil {
		t.Fatal(err)
	}
	result.Body.Close()
	if result.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d", result.StatusCode)
	}
	select {
	case event := <-events:
		if !strings.Contains(event, "Live update") || !strings.Contains(event, `"op":"add"`) || strings.Contains(event, `"servers"`) || len(event) > 1000 {
			t.Fatalf("unexpected project delta: %s", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no change event")
	}
	select {
	case <-events:
		t.Fatal("duplicate snapshot after write")
	case <-time.After(700 * time.Millisecond):
	}
	// Reconnect using the original version: catch up with a diff, never a snapshot.
	replay, _ := http.NewRequest("GET", server.URL+"/api/v1/overview/watch", nil)
	replay.Header = req.Header.Clone()
	replayResponse, err := http.DefaultClient.Do(replay)
	if err != nil {
		t.Fatal(err)
	}
	defer replayResponse.Body.Close()
	scanner := bufio.NewScanner(replayResponse.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") {
			if !strings.Contains(scanner.Text(), "Live update") || strings.Contains(scanner.Text(), `"servers"`) {
				t.Fatalf("bad catch-up: %s", scanner.Text())
			}
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}

}

func TestOverviewWatchMissingBaseline(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()
	req := httptest.NewRequest("GET", "/api/v1/overview/watch", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status %d", response.Code)
	}
	req.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), "projects") {
		t.Fatal("missing baseline must request resync, never send data")
	}
}
