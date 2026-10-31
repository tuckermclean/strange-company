package install

import (
	"context"
	"strings"
	"sync"

	"strange.company/sc/internal/bootstrap/store"
)

// lazyStore is the engine's S3 side before and after the tofu-state bucket exists. Until Attach it behaves like a
// store without a journal: reads find nothing, writes are accepted and dropped, and the run lock the engine takes at
// the start is remembered. Attach binds the real bucket and writes the remembered lock (plan 50 section 14.3 step 3:
// "then acquire the remote run lock"); every journal save from then on also goes to S3.
type lazyStore struct {
	mu    sync.Mutex
	inner store.S3
	lock  *struct {
		key  string
		data []byte
	}
}

var _ store.S3 = (*lazyStore)(nil)

func (l *lazyStore) bound() store.S3 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inner
}

// Attached reports whether a bucket is bound.
func (l *lazyStore) Attached() bool { return l.bound() != nil }

// Attach binds inner and takes the remembered run lock. fix rewrites the remembered lock body (the install uid was
// not known when the engine wrote it). A lock object that already exists is a held run lock (exit 6).
func (l *lazyStore) Attach(ctx context.Context, inner store.S3, fix func([]byte) []byte) error {
	l.mu.Lock()
	pending := l.lock
	l.lock = nil
	l.mu.Unlock()
	if pending != nil {
		data := pending.data
		if fix != nil {
			data = fix(data)
		}
		if _, err := inner.PutIfAbsent(ctx, pending.key, data); err != nil {
			return err
		}
	}
	l.mu.Lock()
	l.inner = inner
	l.mu.Unlock()
	return nil
}

func (l *lazyStore) Get(ctx context.Context, key string) (store.Object, error) {
	if in := l.bound(); in != nil {
		return in.Get(ctx, key)
	}
	return store.Object{}, &store.Error{Kind: store.NotFound, Msg: key}
}

func (l *lazyStore) PutIfAbsent(ctx context.Context, key string, data []byte) (string, error) {
	if in := l.bound(); in != nil {
		return in.PutIfAbsent(ctx, key, data)
	}
	if strings.HasSuffix(key, "locks/bootstrap.lock") {
		l.mu.Lock()
		l.lock = &struct {
			key  string
			data []byte
		}{key, append([]byte(nil), data...)}
		l.mu.Unlock()
	}
	return "", nil
}

func (l *lazyStore) Put(ctx context.Context, key string, data []byte, ifMatch string) (string, error) {
	if in := l.bound(); in != nil {
		return in.Put(ctx, key, data, ifMatch)
	}
	return "", nil
}

func (l *lazyStore) List(ctx context.Context, prefix string) ([]string, error) {
	if in := l.bound(); in != nil {
		return in.List(ctx, prefix)
	}
	return nil, nil
}

func (l *lazyStore) Delete(ctx context.Context, key, ifMatch string) error {
	if in := l.bound(); in != nil {
		return in.Delete(ctx, key, ifMatch)
	}
	return nil
}
