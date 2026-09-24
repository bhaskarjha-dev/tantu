package drop

import (
	"errors"
	"fmt"
	"sync"
)

// ErrTransferQuota is returned when a receiver's bounded transfer budget is
// exhausted. It is deliberately distinct from a protocol/decoding failure so
// callers can present a retryable "receiver busy" outcome.
var ErrTransferQuota = errors.New("transfer quota exceeded")

const maxTransferQuotaBytes int64 = int64(^uint64(0) >> 1)

// TransferQuotaConfig bounds simultaneous QuickDrop reservations. A zero or
// negative limit disables that individual limit. MaxBytes is a reservation
// budget, not a per-frame memory budget: file bytes continue to stream to disk,
// but a peer cannot open an unbounded number of multi-gigabyte writes.
type TransferQuotaConfig struct {
	MaxTransfers        int
	MaxBytes            int64
	MaxPerPeerTransfers int
	MaxPerPeerBytes     int64
}

// TransferQuota is safe for concurrent receiver sessions. Reservations are
// released when a receive attempt ends, including rejected or cancelled
// transfers.
type TransferQuota struct {
	mu                  sync.Mutex
	maxTransfers        int
	maxBytes            int64
	maxPerPeerTransfers int
	maxPerPeerBytes     int64
	activeTransfers     int
	activeBytes         int64
	peers               map[string]transferQuotaUsage
}

type transferQuotaUsage struct {
	transfers int
	bytes     int64
}

// NewTransferQuota creates a transfer quota with an explicit policy. A zero
// value TransferQuota is also safe and represents an unlimited policy; long-
// lived receivers should use DefaultTransferQuota so the intended bounds are
// visible at the construction site.
func NewTransferQuota(cfg TransferQuotaConfig) *TransferQuota {
	return &TransferQuota{
		maxTransfers:        cfg.MaxTransfers,
		maxBytes:            cfg.MaxBytes,
		maxPerPeerTransfers: cfg.MaxPerPeerTransfers,
		maxPerPeerBytes:     cfg.MaxPerPeerBytes,
		peers:               make(map[string]transferQuotaUsage),
	}
}

// DefaultTransferQuota is the conservative Hub/standalone receive policy:
// four active file/text sessions, 8 GiB of reserved transfer bytes, and no more
// than two sessions / 6 GiB from one peer. A single 5 GiB file still fits, but
// a peer cannot multiply that disk commitment without an explicit retry.
func DefaultTransferQuota() *TransferQuota {
	const gib = int64(1024 * 1024 * 1024)
	return NewTransferQuota(TransferQuotaConfig{
		MaxTransfers:        4,
		MaxBytes:            8 * gib,
		MaxPerPeerTransfers: 2,
		MaxPerPeerBytes:     6 * gib,
	})
}

// TransferLease represents one reservation. It is safe to defer Release even
// when Acquire returned a nil lease (the method tolerates nil).
type TransferLease struct {
	quota   *TransferQuota
	peer    string
	bytes   int64
	unknown bool
	once    sync.Once
}

// Acquire reserves capacity for a declared transfer. Unknown-size streams
// reserve one chunk immediately and grow their reservation as bytes arrive, so
// they cannot bypass the aggregate budget without consuming real capacity.
func (q *TransferQuota) Acquire(peer string, declaredBytes int64) (*TransferLease, error) {
	if q == nil {
		return nil, nil
	}
	peer = normalizeQuotaPeer(peer)
	if declaredBytes < 0 {
		return nil, fmt.Errorf("%w: negative declared size", ErrTransferQuota)
	}

	reservation := declaredBytes
	unknown := declaredBytes == 0
	if unknown {
		// Reserve one normal chunk immediately, then grow the reservation as
		// unknown-size data arrives. Reserving the full 5 GiB maximum for an
		// empty or malformed stream would let a peer consume the whole budget
		// without transferring meaningful bytes.
		reservation = DefaultChunkSize
		if q.maxBytes > 0 && reservation > q.maxBytes {
			reservation = q.maxBytes
		}
		if q.maxPerPeerBytes > 0 && reservation > q.maxPerPeerBytes {
			reservation = q.maxPerPeerBytes
		}
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.peers == nil {
		q.peers = make(map[string]transferQuotaUsage)
	}

	usage := q.peers[peer]
	if reservation > 0 && (q.activeBytes > maxTransferQuotaBytes-reservation || usage.bytes > maxTransferQuotaBytes-reservation) {
		return nil, fmt.Errorf("%w: transfer byte counter overflow", ErrTransferQuota)
	}
	if q.maxTransfers > 0 && q.activeTransfers >= q.maxTransfers {
		return nil, fmt.Errorf("%w: maximum active transfers reached", ErrTransferQuota)
	}
	if q.maxPerPeerTransfers > 0 && usage.transfers >= q.maxPerPeerTransfers {
		return nil, fmt.Errorf("%w: peer transfer session limit reached", ErrTransferQuota)
	}
	if q.maxBytes > 0 && (q.activeBytes > q.maxBytes || reservation > q.maxBytes-q.activeBytes) {
		return nil, fmt.Errorf("%w: receiver transfer byte budget reached", ErrTransferQuota)
	}
	if q.maxPerPeerBytes > 0 && (usage.bytes > q.maxPerPeerBytes || reservation > q.maxPerPeerBytes-usage.bytes) {
		return nil, fmt.Errorf("%w: peer transfer byte budget reached", ErrTransferQuota)
	}

	q.activeTransfers++
	q.activeBytes += reservation
	usage.transfers++
	usage.bytes += reservation
	q.peers[peer] = usage
	return &TransferLease{quota: q, peer: peer, bytes: reservation, unknown: unknown}, nil
}

// AddBytes grows an unknown-size transfer reservation as chunks arrive. It is
// a no-op for a known declared size, which was reserved atomically at Acquire.
func (l *TransferLease) AddBytes(n int64) error {
	if l == nil || l.quota == nil || n <= 0 || !l.unknown {
		return nil
	}
	q := l.quota
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.peers == nil {
		return fmt.Errorf("%w: transfer quota is not initialized", ErrTransferQuota)
	}
	usage := q.peers[l.peer]
	if n > 0 && (q.activeBytes > maxTransferQuotaBytes-n || usage.bytes > maxTransferQuotaBytes-n) {
		return fmt.Errorf("%w: transfer byte counter overflow", ErrTransferQuota)
	}
	if q.maxBytes > 0 && (q.activeBytes > q.maxBytes || n > q.maxBytes-q.activeBytes) {
		return fmt.Errorf("%w: receiver transfer byte budget reached", ErrTransferQuota)
	}
	if q.maxPerPeerBytes > 0 && (usage.bytes > q.maxPerPeerBytes || n > q.maxPerPeerBytes-usage.bytes) {
		return fmt.Errorf("%w: peer transfer byte budget reached", ErrTransferQuota)
	}
	q.activeBytes += n
	usage.bytes += n
	q.peers[l.peer] = usage
	l.bytes += n
	return nil
}

// Release returns a reservation to its quota. It is idempotent.
func (l *TransferLease) Release() {
	if l == nil || l.quota == nil {
		return
	}
	l.once.Do(func() {
		q := l.quota
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.activeTransfers > 0 {
			q.activeTransfers--
		}
		if q.activeBytes >= l.bytes {
			q.activeBytes -= l.bytes
		} else {
			q.activeBytes = 0
		}
		usage := q.peers[l.peer]
		if usage.transfers > 0 {
			usage.transfers--
		}
		if usage.bytes >= l.bytes {
			usage.bytes -= l.bytes
		} else {
			usage.bytes = 0
		}
		if usage.transfers == 0 && usage.bytes == 0 {
			delete(q.peers, l.peer)
		} else {
			q.peers[l.peer] = usage
		}
	})
}

// ActiveTransfers and ActiveBytes expose bounded metrics for diagnostics and
// tests without exposing the quota's mutable maps.
func (q *TransferQuota) ActiveTransfers() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.activeTransfers
}

func (q *TransferQuota) ActiveBytes() int64 {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.activeBytes
}

func normalizeQuotaPeer(peer string) string {
	// Peer fingerprints are already short and bounded. Keep the map key
	// bounded even for a custom transport that supplies an arbitrary address.
	if len(peer) > 256 {
		return peer[:256]
	}
	return peer
}
