// Package queue provides a bounded priority queue for event persistence.
// This implements the backpressure mechanism from REVIEW.md.
package queue

import (
	"container/heap"
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Priority levels for events.
const (
	PriorityHigh   = "high"   // message_start, message_stop, usage
	PriorityMedium = "medium" // content_block_start/stop
	PriorityLow    = "low"    // content_block_delta (drop first)
)

// priorityValue maps priority strings to numeric values for comparison.
var priorityValue = map[string]int{
	PriorityHigh:   3,
	PriorityMedium: 2,
	PriorityLow:    1,
}

// QueueItem represents an item in the priority queue.
type QueueItem struct {
	Data      interface{}
	Priority  string
	FlowID    string
	EventType string
	Timestamp time.Time
	index     int // Index in the heap
}

// Stats holds queue statistics.
type Stats struct {
	Size         int
	HighCount    int
	MediumCount  int
	LowCount     int
	DropsTotal   uint64
	DropsLow     uint64
	DropsHigh    uint64
	DropsCritical uint64
}

// Queue is a bounded priority queue with backpressure support.
type Queue struct {
	mu          sync.Mutex
	items       priorityHeap
	maxSize     int
	dropsTotal  uint64
	dropsLow    uint64
	dropsHigh   uint64
	dropsCritical uint64

	// Channels for async processing
	notifyCh chan struct{}
	closeCh  chan struct{}
	closed   bool
}

// NewQueue creates a new bounded priority queue.
func NewQueue(maxSize int) *Queue {
	q := &Queue{
		items:    make(priorityHeap, 0, maxSize),
		maxSize:  maxSize,
		notifyCh: make(chan struct{}, 1),
		closeCh:  make(chan struct{}),
	}
	heap.Init(&q.items)
	return q
}

// Push adds an item to the queue.
// If the queue is full, it applies backpressure rules:
// - Drop LOW priority items first
// - Only drop HIGH priority items as last resort
func (q *Queue) Push(item *QueueItem) (dropped bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return true
	}

	// Check capacity
	if len(q.items) >= q.maxSize {
		dropped = q.evictForSpace(item)
		if dropped {
			return true
		}
	}

	// Add item
	heap.Push(&q.items, item)

	// Notify consumer
	select {
	case q.notifyCh <- struct{}{}:
	default:
	}

	return false
}

// evictForSpace makes room for a new item by evicting lower priority items.
// Returns true if the new item was dropped (couldn't make space).
func (q *Queue) evictForSpace(newItem *QueueItem) bool {
	newPriority := priorityValue[newItem.Priority]
	fillPercent := float64(len(q.items)) / float64(q.maxSize) * 100

	// < 80%: normal operation (shouldn't reach here as we check capacity first)
	// 80-95%: warn, flush faster
	// 95-99%: drop LOW only
	// 100%: drop oldest, even HIGH

	if fillPercent < 95 {
		// Find and remove a LOW priority item
		if q.evictLowest(PriorityLow) {
			atomic.AddUint64(&q.dropsLow, 1)
			atomic.AddUint64(&q.dropsTotal, 1)
			return false
		}
	}

	if fillPercent >= 95 && fillPercent < 100 {
		// Only drop if new item is higher priority than LOW
		if newPriority <= priorityValue[PriorityLow] {
			// Drop the new item
			atomic.AddUint64(&q.dropsLow, 1)
			atomic.AddUint64(&q.dropsTotal, 1)
			return true
		}
		// Evict a LOW item
		if q.evictLowest(PriorityLow) {
			atomic.AddUint64(&q.dropsLow, 1)
			atomic.AddUint64(&q.dropsTotal, 1)
			return false
		}
	}

	// Queue is completely full - last resort
	// Drop oldest item regardless of priority
	if len(q.items) > 0 {
		oldest := heap.Pop(&q.items).(*QueueItem)
		if priorityValue[oldest.Priority] >= priorityValue[PriorityHigh] {
			atomic.AddUint64(&q.dropsHigh, 1)
			atomic.AddUint64(&q.dropsCritical, 1)
		}
		atomic.AddUint64(&q.dropsTotal, 1)
		return false
	}

	return true
}

// evictLowest removes the lowest priority item at or below the given priority.
// Returns true if an item was evicted.
func (q *Queue) evictLowest(maxPriority string) bool {
	maxPrio := priorityValue[maxPriority]

	// Find lowest priority item
	lowestIdx := -1
	lowestPrio := 999
	var oldestTime time.Time

	for i, item := range q.items {
		itemPrio := priorityValue[item.Priority]
		if itemPrio <= maxPrio {
			// Prefer lower priority, then older timestamp
			if itemPrio < lowestPrio || (itemPrio == lowestPrio && (lowestIdx == -1 || item.Timestamp.Before(oldestTime))) {
				lowestIdx = i
				lowestPrio = itemPrio
				oldestTime = item.Timestamp
			}
		}
	}

	if lowestIdx >= 0 {
		heap.Remove(&q.items, lowestIdx)
		return true
	}
	return false
}

// Pop removes and returns the highest priority item.
// Returns nil if the queue is empty.
func (q *Queue) Pop() *QueueItem {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.items) == 0 {
		return nil
	}

	return heap.Pop(&q.items).(*QueueItem)
}

// PopBatch removes and returns up to n items.
func (q *Queue) PopBatch(n int) []*QueueItem {
	q.mu.Lock()
	defer q.mu.Unlock()

	if n <= 0 || len(q.items) == 0 {
		return nil
	}

	count := n
	if count > len(q.items) {
		count = len(q.items)
	}

	result := make([]*QueueItem, count)
	for i := 0; i < count; i++ {
		result[i] = heap.Pop(&q.items).(*QueueItem)
	}

	return result
}

// Len returns the current queue size.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Stats returns queue statistics.
func (q *Queue) Stats() Stats {
	q.mu.Lock()
	defer q.mu.Unlock()

	stats := Stats{
		Size:          len(q.items),
		DropsTotal:    atomic.LoadUint64(&q.dropsTotal),
		DropsLow:      atomic.LoadUint64(&q.dropsLow),
		DropsHigh:     atomic.LoadUint64(&q.dropsHigh),
		DropsCritical: atomic.LoadUint64(&q.dropsCritical),
	}

	for _, item := range q.items {
		switch item.Priority {
		case PriorityHigh:
			stats.HighCount++
		case PriorityMedium:
			stats.MediumCount++
		case PriorityLow:
			stats.LowCount++
		}
	}

	return stats
}

// FillPercent returns the current fill percentage (0-100).
func (q *Queue) FillPercent() float64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return float64(len(q.items)) / float64(q.maxSize) * 100
}

// NotifyCh returns a channel that receives notifications when items are added.
func (q *Queue) NotifyCh() <-chan struct{} {
	return q.notifyCh
}

// Close closes the queue.
func (q *Queue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if !q.closed {
		q.closed = true
		close(q.closeCh)
	}
}

// Wait blocks until the context is cancelled or the queue is closed.
// Used by consumers to wait for items.
func (q *Queue) Wait(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-q.closeCh:
		return false
	case <-q.notifyCh:
		return true
	}
}

// priorityHeap implements heap.Interface for priority queue.
type priorityHeap []*QueueItem

func (h priorityHeap) Len() int { return len(h) }

func (h priorityHeap) Less(i, j int) bool {
	// Higher priority first
	pi := priorityValue[h[i].Priority]
	pj := priorityValue[h[j].Priority]
	if pi != pj {
		return pi > pj
	}
	// Same priority: older first (FIFO within priority)
	return h[i].Timestamp.Before(h[j].Timestamp)
}

func (h priorityHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *priorityHeap) Push(x interface{}) {
	item := x.(*QueueItem)
	item.index = len(*h)
	*h = append(*h, item)
}

func (h *priorityHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*h = old[0 : n-1]
	return item
}
