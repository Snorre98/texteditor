// Package shard holds the workspace shard Manager — the lazy lifecycle over
// per-workspace context-state storage (ADR-0049 §5).
//
// A workspace shard is <data>/workspaces/<workspaceID>/{index.db,sessions.db,
// meter.db}: the Retriever, Session store, and Token meter instance that belong
// to one workspace. Shards open lazily on first use, migrate per shard, and
// close on least-recently-used eviction once the open-handle cap is exceeded.
// Document identity, git history, and the worktree stay global; only context
// state is sharded.
//
// A Services lease is reference-counted: a shard with an outstanding lease is
// never evicted, so a long-running turn or corpus job cannot have its database
// closed underneath it.
package shard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"

	"texteditor/internal/meter"
	"texteditor/internal/retriever"
	"texteditor/internal/session"
)

// DefaultCap is the open-shard bound (ADR-0049 §5: lazy open, close LRU).
const DefaultCap = 4

// Services are the workspace-scoped service instances of one shard.
type Services struct {
	Retriever retriever.Interface
	Sessions  session.Interface
	Meter     meter.Interface
}

// Resolver is the sealed seam the loop and API server use to acquire a
// workspace's services (ADR-0049 §5).
type Resolver interface {
	Services(ctx context.Context, workspaceID string) (*Lease, error)
}

// Lease is a reference-counted handle to one shard's services. Release must be
// called when the caller is done; a leased shard is exempt from LRU eviction.
type Lease struct {
	Services
	release func()
	once    sync.Once
}

// Release returns the lease. Safe to call more than once.
func (l *Lease) Release() {
	if l == nil || l.release == nil {
		return
	}
	l.once.Do(l.release)
}

// NewLease wraps services in a lease with a no-op release. It exists for tests
// and composition seams that do not own the shard lifecycle.
func NewLease(s Services) *Lease { return &Lease{Services: s} }

// ctxKey carries the turn's workspace services to tool handlers.
type ctxKey struct{}

// WithServices returns a context carrying the turn's shard services.
func WithServices(ctx context.Context, s *Services) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// ServicesFromContext returns the turn's shard services, if present.
func ServicesFromContext(ctx context.Context) (*Services, bool) {
	s, ok := ctx.Value(ctxKey{}).(*Services)
	return s, ok
}

// Options configures the Manager.
type Options struct {
	// DataDir is the --data root; shards live under <DataDir>/workspaces/<id>.
	DataDir string
	// Bus receives meter events (the shard's meter instance).
	Bus meter.Emitter
	// NewRetriever builds the shard's Retriever over its index.db. The
	// composition root closes over Fleet/Provider/Document/Filesystem/Chunker;
	// tests inject a stub.
	NewRetriever func(db *sql.DB) retriever.Interface
	// Cap bounds open shards; <= 0 uses DefaultCap.
	Cap int
}

// Manager owns the shard lifecycle.
type Manager struct {
	opts Options

	mu     sync.Mutex
	open   map[string]*entry
	order  []string // workspace ids, least recently used first
	closed bool
}

type entry struct {
	services Services
	dbs      []*sql.DB
	refs     int
}

// New returns a Manager over opts.
func New(opts Options) *Manager {
	if opts.Cap <= 0 {
		opts.Cap = DefaultCap
	}
	return &Manager{
		opts: opts,
		open: map[string]*entry{},
	}
}

// Services opens (or reuses) the workspace's shard and returns a lease.
func (m *Manager) Services(ctx context.Context, workspaceID string) (*Lease, error) {
	if workspaceID == "" {
		return nil, errors.New("workspace-id-required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("shard-manager-closed")
	}
	e, ok := m.open[workspaceID]
	if !ok {
		var err error
		e, err = m.openShard(ctx, workspaceID)
		if err != nil {
			return nil, err
		}
		m.open[workspaceID] = e
	}
	e.refs++
	m.touchLocked(workspaceID)
	m.evictLocked(workspaceID)
	return &Lease{Services: e.services, release: func() { m.release(workspaceID) }}, nil
}

// release decrements the refcount and updates recency.
func (m *Manager) release(workspaceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.open[workspaceID]
	if !ok {
		return
	}
	if e.refs > 0 {
		e.refs--
	}
	m.touchLocked(workspaceID)
	m.evictLocked("")
}

// touchLocked moves the workspace id to the most-recent end of the LRU order.
func (m *Manager) touchLocked(workspaceID string) {
	for i, id := range m.order {
		if id == workspaceID {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	m.order = append(m.order, workspaceID)
}

// evictLocked closes idle shards beyond the cap, least-recently-used first.
// The just-acquired workspace (refs > 0) is never evicted.
func (m *Manager) evictLocked(keep string) {
	for len(m.open) > m.opts.Cap {
		victim := ""
		for _, id := range m.order {
			if id == keep {
				continue
			}
			if e, ok := m.open[id]; ok && e.refs == 0 {
				victim = id
				break
			}
		}
		if victim == "" {
			return // all other shards are leased; stay over cap until release
		}
		m.closeLocked(victim)
	}
}

// closeLocked closes one shard's databases and removes it from the map.
func (m *Manager) closeLocked(workspaceID string) {
	e, ok := m.open[workspaceID]
	if !ok {
		return
	}
	for _, db := range e.dbs {
		_ = db.Close()
	}
	delete(m.open, workspaceID)
	for i, id := range m.order {
		if id == workspaceID {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
}

// openShard creates the shard directory, opens its three databases, and
// migrates the sessions/meter schemas (the Retriever migrates lazily when its
// embedding dimension is learned).
func (m *Manager) openShard(ctx context.Context, workspaceID string) (*entry, error) {
	dir := filepath.Join(m.opts.DataDir, "workspaces", workspaceID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("shard dir: %w", err)
	}

	open := func(name string) (*sql.DB, error) {
		db, err := sql.Open("sqlite", filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		return db, nil
	}

	e := &entry{}
	closeAll := func() {
		for _, db := range e.dbs {
			_ = db.Close()
		}
	}

	sessDB, err := open("sessions.db")
	if err != nil {
		return nil, err
	}
	e.dbs = append(e.dbs, sessDB)
	if err := session.Migrate(ctx, sessDB); err != nil {
		closeAll()
		return nil, fmt.Errorf("sessions.db: %w", err)
	}

	meterDB, err := open("meter.db")
	if err != nil {
		closeAll()
		return nil, err
	}
	e.dbs = append(e.dbs, meterDB)
	if err := meter.Migrate(ctx, meterDB); err != nil {
		closeAll()
		return nil, fmt.Errorf("meter.db: %w", err)
	}

	indexDB, err := open("index.db")
	if err != nil {
		closeAll()
		return nil, err
	}
	e.dbs = append(e.dbs, indexDB)
	// Materialize index.db now (sql.Open is lazy) so the shard layout exists
	// from first open; the Retriever migrates its schema on first use.
	if err := indexDB.PingContext(ctx); err != nil {
		closeAll()
		return nil, fmt.Errorf("index.db: %w", err)
	}

	e.services = Services{
		Retriever: m.opts.NewRetriever(indexDB),
		Sessions:  session.New(sessDB),
		Meter:     meter.New(meterDB, m.opts.Bus),
	}
	return e, nil
}

// IsOpen reports whether a workspace's shard is currently open (tests/ops).
func (m *Manager) IsOpen(workspaceID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.open[workspaceID]
	return ok
}

// OpenCount returns the number of open shards.
func (m *Manager) OpenCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.open)
}

// Close closes every shard (shutdown). Outstanding leases become invalid.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for id := range m.open {
		m.closeLocked(id)
	}
	m.order = nil
	return nil
}
