package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// Lock is a per-ledger exclusive lock. It combines an in-process mutex (so
// goroutines in one process exclude each other even on platforms where flock
// is per-process) with an OS file lock (so separate CLI processes exclude each
// other).
type Lock struct {
	fl     *flock.Flock
	mutex  *sync.Mutex
	ledger string
}

var (
	processLocksMu sync.Mutex
	processLocks   = map[string]*sync.Mutex{}
)

func processMutex(key string) *sync.Mutex {
	processLocksMu.Lock()
	defer processLocksMu.Unlock()
	mu, ok := processLocks[key]
	if !ok {
		mu = &sync.Mutex{}
		processLocks[key] = mu
	}
	return mu
}

func lockKey(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return abs
}

// AcquireLock takes the ledger lock, retrying until timeout. A timeout returns
// a BUSY coded error.
func AcquireLock(dir string, timeout time.Duration) (*Lock, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, Wrap(CodeGeneral, "create ledger directory", err)
	}
	key := lockKey(dir)
	mu := processMutex(key)

	deadline := time.Now().Add(timeout)
	for {
		if mu.TryLock() {
			break
		}
		if time.Now().After(deadline) {
			return nil, Errorf(CodeBusy, "ledger %s is busy (another writer holds the in-process lock); retry shortly", key)
		}
		time.Sleep(20 * time.Millisecond)
	}

	lockPath := filepath.Join(key, ".lock")
	fl := flock.New(lockPath)
	for {
		ok, err := fl.TryLock()
		if err != nil {
			_ = fl.Close()
			mu.Unlock()
			return nil, Wrap(CodeBusy, "acquire ledger file lock", err)
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			mu.Unlock()
			_ = fl.Close()
			return nil, Errorf(CodeBusy, "ledger %s is busy (lock held by another process); retry shortly", key)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return &Lock{fl: fl, mutex: mu, ledger: key}, nil
}

func (l *Lock) Release() error {
	var errs []error
	if l.fl != nil {
		if err := l.fl.Unlock(); err != nil {
			errs = append(errs, err)
		}
		if err := l.fl.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if l.mutex != nil {
		l.mutex.Unlock()
	}
	if len(errs) > 0 {
		return fmt.Errorf("release lock: %w", errors.Join(errs...))
	}
	return nil
}
