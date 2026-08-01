package auth

import (
	"sync"
	"sync/atomic"
)

// SubscribeActiveRequestChanges subscribes to coalesced active-request state changes.
// Notifications are best-effort and never block proxy request processing; consumers
// must read a fresh snapshot after every notification.
func (s *Store) SubscribeActiveRequestChanges() (<-chan struct{}, func()) {
	changes := make(chan struct{}, 1)
	if s == nil {
		return changes, func() {}
	}

	id := atomic.AddInt64(&s.activeRequestSubSeq, 1)
	s.activeRequestSubsMu.Lock()
	if s.activeRequestSubs == nil {
		s.activeRequestSubs = make(map[int64]chan struct{})
	}
	s.activeRequestSubs[id] = changes
	s.activeRequestSubsMu.Unlock()

	var once sync.Once
	return changes, func() {
		once.Do(func() {
			s.activeRequestSubsMu.Lock()
			delete(s.activeRequestSubs, id)
			s.activeRequestSubsMu.Unlock()
		})
	}
}

func (s *Store) notifyActiveRequestChanges() {
	if s == nil {
		return
	}
	s.activeRequestSubsMu.Lock()
	subscribers := make([]chan struct{}, 0, len(s.activeRequestSubs))
	for _, changes := range s.activeRequestSubs {
		subscribers = append(subscribers, changes)
	}
	s.activeRequestSubsMu.Unlock()

	for _, changes := range subscribers {
		select {
		case changes <- struct{}{}:
		default:
		}
	}
}
