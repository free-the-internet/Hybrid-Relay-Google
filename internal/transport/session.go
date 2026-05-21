package transport

import (
	"sync"
	"time"
)

// Direction indicates if a file is req (client to server) or res (server to client)
type Direction string

const (
	DirReq Direction = "req"
	DirRes Direction = "res"
)

// Session represents an active proxy connection mapped to files.
type Session struct {
	ID           string
	mu           sync.Mutex
	txBuf        []byte
	txSeq        uint64
	txPending    map[uint64]*pendingEnvelope
	rxSeq        uint64
	rxQueue      map[uint64]*Envelope
	lastActivity time.Time
	lastGuardLog time.Time
	ackDirty     bool
	lastAckCtrl  time.Time
	lastRetxLog  time.Time
	lastRetxPend int
	closed       bool
	closeSent    bool
	closeStart   time.Time
	rxClosed     bool // Safely tracks if RxChan was successfully closed
	TargetAddr   string
	ClientID     string

	// Backpressure: blocked when txBuf is too large
	txCond *sync.Cond

	// App channel for receiving data downloaded from remote
	RxChan chan []byte
}

type pendingEnvelope struct {
	env    Envelope
	sentAt time.Time
}

func NewSession(id string) *Session {
	s := &Session{
		ID:           id,
		txPending:    make(map[uint64]*pendingEnvelope),
		rxQueue:      make(map[uint64]*Envelope),
		lastActivity: time.Now(),
		lastRetxPend: -1,
		RxChan:       make(chan []byte, 1024),
	}
	s.txCond = sync.NewCond(&s.mu)
	return s
}

func (s *Session) EnqueueTx(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// BACKPRESSURE: Block if txBuf is larger than 2MB
	// This prevents memory explosion when uploading through the proxy
	for len(s.txBuf) > 2*1024*1024 && !s.closed {
		s.txCond.Wait()
	}

	s.txBuf = append(s.txBuf, data...)
	s.lastActivity = time.Now()
}

func (s *Session) ClearTx() {
	s.mu.Lock()
	s.txBuf = nil
	s.txCond.Broadcast() // Wake up any writers blocked on backpressure
	s.mu.Unlock()
}

// RequestClose marks the local write side closed and starts close-drain timing.
func (s *Session) RequestClose() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true
	if s.closeStart.IsZero() {
		s.closeStart = time.Now()
	}
	s.txCond.Broadcast()
}

// ApplyRemoteAck drops any sent envelopes acknowledged cumulatively by peer.
// nextSeq means peer received all local seq values < nextSeq.
// Returns how many pending envelopes were released.
func (s *Session) ApplyRemoteAck(nextSeq uint64) uint64 {
	if nextSeq == 0 {
		return 0
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var released uint64
	for seq := range s.txPending {
		if seq < nextSeq {
			delete(s.txPending, seq)
			released++
		}
	}
	if released > 0 {
		s.txCond.Broadcast()
	}

	return released
}

func (s *Session) ProcessRx(env *Envelope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastActivity = time.Now()

	if s.rxClosed {
		return // Ignore packets if the channel is already safely closed
	}

	if env.Seq == s.rxSeq {
		s.ackDirty = true
		if len(env.Payload) > 0 {
			s.RxChan <- env.Payload
		}
		s.rxSeq++
		if env.Close {
			s.rxClosed = true
			s.closed = true
			if s.closeStart.IsZero() {
				s.closeStart = time.Now()
			}
			close(s.RxChan)
			return
		}

		// process any queued future packets
		for {
			if nextEnv, ok := s.rxQueue[s.rxSeq]; ok {
				s.ackDirty = true
				if len(nextEnv.Payload) > 0 {
					s.RxChan <- nextEnv.Payload
				}
				delete(s.rxQueue, s.rxSeq)
				s.rxSeq++
				if nextEnv.Close {
					s.rxClosed = true
					s.closed = true
					if s.closeStart.IsZero() {
						s.closeStart = time.Now()
					}
					close(s.RxChan)
					return
				}
			} else {
				break
			}
		}
	} else if env.Seq > s.rxSeq {
		s.ackDirty = true
		if _, exists := s.rxQueue[env.Seq]; !exists {
			s.rxQueue[env.Seq] = env
		}
	} else {
		// Duplicate/old packet: signal peer with current cumulative ack.
		s.ackDirty = true
	}
}
