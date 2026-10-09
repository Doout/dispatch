package workflowrunner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	secretcrypto "github.com/doout/dispatch/internal/crypto"
)

type Execute func(context.Context, Request, string, func(string)) Result

type Worker struct {
	directory string
	vault     *secretcrypto.Vault
	lock      *os.File
	execute   Execute
	mu        sync.Mutex
}
type workerReceipt struct{ Digest, State, Result string }

func OpenWorker(directory, node string, execute Execute) (*Worker, error) {
	if !filepath.IsAbs(directory) || !identifier.MatchString(node) || execute == nil {
		return nil, errors.New("worker state requires an absolute directory, node identity and executor")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("worker state directory must be private")
	}
	lock, err := os.OpenFile(filepath.Join(directory, "worker.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another worker owns this state directory")
	}
	w := &Worker{directory: directory, lock: lock, execute: execute}
	ok := false
	defer func() {
		if !ok {
			w.Close()
		}
	}()
	keyPath := filepath.Join(directory, "key")
	if _, err = os.Lstat(keyPath); os.IsNotExist(err) {
		key := make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		err = w.save("key", []byte(base64.StdEncoding.EncodeToString(key)))
		clear(key)
		if err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if info, err = os.Lstat(keyPath); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("worker encryption key must be private")
	}
	w.vault, err = secretcrypto.OpenFile(keyPath)
	if err != nil {
		return nil, err
	}
	binding, err := os.ReadFile(filepath.Join(directory, "node"))
	if os.IsNotExist(err) {
		err = w.save("node", []byte(node))
	} else if err == nil && string(binding) != node {
		err = errors.New("worker state belongs to another node")
	}
	if err != nil {
		return nil, err
	}
	ok = true
	return w, nil
}
func (w *Worker) Close() error { return w.lock.Close() }
func (w *Worker) save(name string, raw []byte) error {
	file, err := os.CreateTemp(w.directory, ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(file.Name(), filepath.Join(w.directory, name)); err != nil {
		return err
	}
	directory, err := os.Open(w.directory)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
func (w *Worker) Run(ctx context.Context, j LeasedJob, progress func(string)) Result {
	w.mu.Lock()
	defer w.mu.Unlock()
	unknown := func(message string) Result { return Result{State: "unknown", Error: message} }
	if !identifier.MatchString(j.ID) || len(j.Digest) != 64 {
		return unknown("invalid worker operation identity")
	}
	if err := j.Request.Validate(); err != nil {
		return Result{State: "failed", Error: err.Error()}
	}
	request, err := json.Marshal(j.Request)
	if err != nil {
		return unknown("invalid worker request")
	}
	digest := sha256.Sum256(request)
	clear(request)
	if hex.EncodeToString(digest[:]) != j.Digest {
		return unknown("worker request does not match its accepted digest")
	}
	raw, err := os.ReadFile(filepath.Join(w.directory, j.ID+".json"))
	var receipt workerReceipt
	if err == nil {
		if json.Unmarshal(raw, &receipt) != nil || receipt.Digest != j.Digest {
			return unknown("worker receipt identity changed")
		}
		if receipt.State != "complete" {
			return unknown("worker was interrupted; inspect effects before starting another operation")
		}
		result, err := w.vault.Decrypt("receipt:"+j.ID, receipt.Result)
		if err != nil {
			return unknown("worker receipt could not be read")
		}
		defer clear(result)
		var value Result
		if json.Unmarshal(result, &value) != nil {
			return unknown("worker receipt is invalid")
		}
		return value
	}
	if !os.IsNotExist(err) || j.Attempt != 1 {
		return unknown("worker receipt is unavailable; the operation was not repeated")
	}
	receipt = workerReceipt{Digest: j.Digest, State: "running"}
	raw, _ = json.Marshal(receipt)
	if err = w.save(j.ID+".json", raw); err != nil {
		return Result{State: "failed", Error: "worker could not save the operation receipt"}
	}
	workspace, err := os.MkdirTemp(w.directory, "job-")
	if err != nil {
		return unknown("worker could not prepare its workspace")
	}
	defer os.RemoveAll(workspace)
	result := w.execute(ctx, j.Request, workspace, progress)
	result.Log, result.Error = j.Request.Redact(result.Log), j.Request.Redact(result.Error)
	raw, err = json.Marshal(result)
	if err != nil {
		return unknown("worker result could not be encoded")
	}
	defer clear(raw)
	receipt.Result, err = w.vault.Encrypt("receipt:"+j.ID, raw)
	if err != nil {
		return unknown("worker result could not be encrypted")
	}
	receipt.State = "complete"
	encoded, _ := json.Marshal(receipt)
	if err = w.save(j.ID+".json", encoded); err != nil {
		return unknown("worker result could not be saved")
	}
	return result
}
