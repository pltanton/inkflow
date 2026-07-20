package importer

import "sync"

// LockManager serializes work that changes the same vault note. Entries are
// removed once their last holder leaves so the bookkeeping does not grow with
// the number of imported files.
type LockManager struct {
	mu    sync.Mutex
	locks map[string]*keyLock
}

type keyLock struct {
	mu   sync.Mutex
	refs int
}

func NewLockManager() *LockManager {
	return &LockManager{locks: make(map[string]*keyLock)}
}

// Lock returns an unlock function for key.
func (m *LockManager) Lock(key string) func() {
	m.mu.Lock()
	l := m.locks[key]
	if l == nil {
		l = &keyLock{}
		m.locks[key] = l
	}
	l.refs++
	m.mu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		m.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(m.locks, key)
		}
		m.mu.Unlock()
	}
}
