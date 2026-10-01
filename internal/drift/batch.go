package drift

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type batchKey struct{}

type batchConnection struct {
	connection Connection
	err        error
}

type batchRead struct {
	object *unstructured.Unstructured
	err    error
	at     time.Time
}

// A batch shares discovery and repeated reads only for its lifetime. Connections
// remain scoped to the registered target and its current credentials.
type checkBatch struct {
	service     *Service
	mu          sync.Mutex
	connections map[[32]byte]batchConnection
	reads       map[string]batchRead
}

func (s *Service) Batch(ctx context.Context) (context.Context, func()) {
	b := &checkBatch{service: s, connections: map[[32]byte]batchConnection{}, reads: map[string]batchRead{}}
	return context.WithValue(ctx, batchKey{}, b), func() {
		for _, entry := range b.connections {
			if entry.err == nil && entry.connection.Close != nil {
				entry.connection.Close()
			}
		}
	}
}

func connectionKey(server core.Server) [32]byte {
	var privateConfig, privateCA string
	if server.Kubernetes != nil {
		privateConfig, privateCA = server.Kubernetes.KubeconfigData, server.Kubernetes.CertificateAuthorityData
	}
	raw, _ := json.Marshal(struct {
		ID            string
		Configuration *core.KubernetesServerConfig
		Credentials   string
		CA            string
	}{server.ID, server.Kubernetes, privateConfig, privateCA})
	return sha256.Sum256(raw)
}

func (s *Service) connection(ctx context.Context, server core.Server) (Connection, func(), error) {
	b, _ := ctx.Value(batchKey{}).(*checkBatch)
	if b == nil || b.service != s {
		conn, err := s.Connect(ctx, server)
		return conn, conn.Close, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	key := connectionKey(server)
	entry, ok := b.connections[key]
	if !ok {
		entry.connection, entry.err = s.Connect(ctx, server)
		b.connections[key] = entry
	}
	return entry.connection, func() {}, entry.err
}

func (s *Service) read(ctx context.Context, conn Connection, server core.Server, object *unstructured.Unstructured, namespace string) (*unstructured.Unstructured, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, err := conn.resource(object, namespace)
	if err != nil {
		return nil, err
	}
	b, _ := ctx.Value(batchKey{}).(*checkBatch)
	if b == nil || b.service != s {
		return client.Get(ctx, object.GetName(), metav1.GetOptions{})
	}
	// resource() resolves the namespace before constructing the cache key.
	raw, _ := json.Marshal([]any{connectionKey(server), object.GetAPIVersion(), object.GetKind(), object.GetNamespace(), object.GetName()})
	key := string(raw)
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.reads[key]
	if !ok || time.Since(entry.at) > 30*time.Second {
		entry.object, entry.err = client.Get(ctx, object.GetName(), metav1.GetOptions{})
		entry.at = time.Now()
		if !errors.Is(entry.err, context.Canceled) && !errors.Is(entry.err, context.DeadlineExceeded) {
			b.reads[key] = entry
		}
	}
	if entry.object == nil {
		return nil, entry.err
	}
	return entry.object.DeepCopy(), entry.err
}
