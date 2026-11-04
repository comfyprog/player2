// Package queue implements player2's in-memory playback queue.
//
// A Queue owns an ordered slice of Items and a current position. Item IDs are
// assigned by the queue and remain stable across removals and moves. Shuffle
// playback uses a precomputed order of IDs so that mutations do not disturb
// the existing play order. Repeat-one affects only automatic advancement.
//
// The model performs no I/O, starts no goroutines, and is not safe for
// concurrent use; the caller serializes access.
package queue

import (
	"fmt"
	"math/rand"
	"time"
)

// Item is one playable queue entry. CuePath, CueTrack, Start, and Duration are
// set for CUE tracks; plain files leave them zero.
//
// Durations are encoded as integer nanoseconds by encoding/json.
type Item struct {
	ID       uint64        `json:"id"`
	Path     string        `json:"path"`
	CuePath  string        `json:"cue_path"`
	CueTrack int           `json:"cue_track"`
	Start    time.Duration `json:"start"`
	Duration time.Duration `json:"duration"`
	Title    string        `json:"title"`
	Artist   string        `json:"artist"`
	Album    string        `json:"album"`
	TrackNo  int           `json:"track_no"`
}

// RepeatMode controls what happens at the end of the queue.
type RepeatMode int

// Repeat modes. RepeatOne only affects automatic advancement; a manual Next
// advances normally.
const (
	RepeatOff RepeatMode = iota
	RepeatAll
	RepeatOne
)

// Snapshot is the serializable state of a Queue. Items are copied, not
// aliased, by Queue.Snapshot.
type Snapshot struct {
	Items   []Item     `json:"items"`
	Current int        `json:"current"`
	Shuffle bool       `json:"shuffle"`
	Repeat  RepeatMode `json:"repeat"`
}

// Queue is an ordered playlist with a current position.
type Queue struct {
	items   []Item
	current int
	shuffle bool
	repeat  RepeatMode

	// order is the precomputed shuffle playback order, stored as item IDs so
	// moves do not require reordering. It is nil when shuffle is off.
	order []uint64

	// nextID is the last ID handed out. IDs start at 1.
	nextID uint64

	rng *rand.Rand
}

// New returns an empty queue with a time-seeded shuffle source.
func New() *Queue {
	return newWithRand(rand.New(rand.NewSource(time.Now().UnixNano())))
}

// newWithRand returns an empty queue using r for shuffle permutations. It is
// unexported so tests in this package can inject a deterministic source.
func newWithRand(r *rand.Rand) *Queue {
	if r == nil {
		r = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	return &Queue{
		current: -1,
		rng:     r,
	}
}

// assignID returns the next stable ID. IDs are never reused.
func (q *Queue) assignID() uint64 {
	q.nextID++
	return q.nextID
}

// Append adds items to the end of the queue, assigning fresh IDs. Appending to
// an empty queue makes the first appended item current.
func (q *Queue) Append(items ...Item) {
	if len(items) == 0 {
		return
	}
	start := len(q.items)
	for _, item := range items {
		item.ID = q.assignID()
		q.items = append(q.items, item)
	}
	if start == 0 {
		q.current = 0
	}
	if q.shuffle {
		for _, item := range q.items[start:] {
			q.order = append(q.order, item.ID)
		}
	}
}

// Replace discards the queue contents and installs items, assigning fresh IDs.
// The first item becomes current, or current becomes -1 for an empty queue.
func (q *Queue) Replace(items ...Item) {
	q.items = make([]Item, len(items))
	for i, item := range items {
		item.ID = q.assignID()
		q.items[i] = item
	}
	if len(q.items) == 0 {
		q.current = -1
	} else {
		q.current = 0
	}
	q.rebuildOrder()
}

// InsertAfterCurrent inserts items immediately after the current item. When
// there is no current item it appends to the end; on an empty queue that also
// makes the first inserted item current.
func (q *Queue) InsertAfterCurrent(items ...Item) {
	if len(items) == 0 {
		return
	}
	if q.current < 0 || q.current >= len(q.items) {
		q.Append(items...)
		return
	}

	inserted := make([]Item, len(items))
	for i, item := range items {
		item.ID = q.assignID()
		inserted[i] = item
	}

	pos := q.current + 1
	merged := make([]Item, 0, len(q.items)+len(inserted))
	merged = append(merged, q.items[:pos]...)
	merged = append(merged, inserted...)
	merged = append(merged, q.items[pos:]...)
	q.items = merged

	if q.shuffle {
		for _, item := range inserted {
			q.order = append(q.order, item.ID)
		}
	}
}

// Remove deletes the item at index and returns it. Invalid indexes return
// false and leave the queue unchanged.
//
// When the removed item was current, the item that takes its place becomes
// current; removing the last item selects the previous one.
func (q *Queue) Remove(index int) (Item, bool) {
	if index < 0 || index >= len(q.items) {
		return Item{}, false
	}

	removed := q.items[index]
	n := len(q.items)
	copy(q.items[index:], q.items[index+1:])
	q.items[n-1] = Item{}
	q.items = q.items[:n-1]

	switch {
	case len(q.items) == 0:
		q.current = -1
	case index < q.current:
		q.current--
	case index == q.current:
		if index >= len(q.items) {
			q.current = len(q.items) - 1
		} else {
			q.current = index
		}
	}

	if q.shuffle {
		q.removeFromOrder(removed.ID)
	}
	return removed, true
}

// Move shifts the item at index by delta positions. It returns false for an
// invalid index, a zero delta, or a target outside the queue. The current item
// tracks the same item when it, or the moved item, crosses the current
// position.
func (q *Queue) Move(index, delta int) bool {
	if index < 0 || index >= len(q.items) || delta == 0 {
		return false
	}
	target := index + delta
	if target < 0 || target >= len(q.items) {
		return false
	}

	item := q.items[index]
	n := len(q.items)
	copy(q.items[index:], q.items[index+1:])
	q.items[n-1] = Item{}
	q.items = q.items[:n-1]

	q.items = append(q.items, Item{})
	copy(q.items[target+1:], q.items[target:])
	q.items[target] = item

	switch {
	case q.current == index:
		q.current = target
	case index < q.current && target >= q.current:
		q.current--
	case index > q.current && target <= q.current:
		q.current++
	}
	return true
}

// Clear removes all items and the shuffle order. Shuffle and repeat settings
// are retained.
func (q *Queue) Clear() {
	q.items = nil
	q.current = -1
	q.order = nil
}

// SetCurrent selects the item at index. Only indexes within the queue are
// accepted.
func (q *Queue) SetCurrent(index int) bool {
	if index < 0 || index >= len(q.items) {
		return false
	}
	q.current = index
	return true
}

// Current returns the current item, if any.
func (q *Queue) Current() (Item, bool) {
	if q.current < 0 || q.current >= len(q.items) {
		return Item{}, false
	}
	return q.items[q.current], true
}

// Next advances to the next item in playback order. A queue without a current
// item starts at the first item in playback order.
//
// With auto set and repeat-one enabled, the current item is returned again
// without moving. At the end of the play order repeat-all wraps to the first
// item; otherwise Next reports failure and leaves the current item unchanged.
func (q *Queue) Next(auto bool) (Item, bool) {
	if len(q.items) == 0 {
		return Item{}, false
	}
	order := q.playbackOrder()
	if len(order) == 0 {
		return Item{}, false
	}

	if q.current < 0 || q.current >= len(q.items) {
		return q.selectID(order[0])
	}

	pos := q.positionOf(q.items[q.current].ID, order)
	if pos < 0 {
		return Item{}, false
	}

	if auto && q.repeat == RepeatOne {
		return q.items[q.current], true
	}
	if pos+1 < len(order) {
		return q.selectID(order[pos+1])
	}
	if q.repeat == RepeatAll {
		return q.selectID(order[0])
	}
	return Item{}, false
}

// Prev moves to the previous item in playback order. A queue without a current
// item starts at the first item in playback order. At the start of the play
// order repeat-all wraps to the last item; otherwise Prev reports failure.
// Repeat-one behaves like repeat-off for manual previous.
func (q *Queue) Prev() (Item, bool) {
	if len(q.items) == 0 {
		return Item{}, false
	}
	order := q.playbackOrder()
	if len(order) == 0 {
		return Item{}, false
	}

	if q.current < 0 || q.current >= len(q.items) {
		return q.selectID(order[0])
	}

	pos := q.positionOf(q.items[q.current].ID, order)
	if pos < 0 {
		return Item{}, false
	}
	if pos > 0 {
		return q.selectID(order[pos-1])
	}
	if q.repeat == RepeatAll {
		return q.selectID(order[len(order)-1])
	}
	return Item{}, false
}

// SetShuffle enables or disables shuffled playback. Enabling builds a fresh
// permutation once; enabling an already shuffled queue is a no-op.
func (q *Queue) SetShuffle(enabled bool) {
	if q.shuffle == enabled {
		return
	}
	q.shuffle = enabled
	if enabled {
		q.buildOrder()
	} else {
		q.order = nil
	}
}

// SetRepeat sets the repeat mode. Invalid modes are ignored.
func (q *Queue) SetRepeat(mode RepeatMode) {
	if mode < RepeatOff || mode > RepeatOne {
		return
	}
	q.repeat = mode
}

// CycleRepeat advances off to all to one to off and returns the new mode.
func (q *Queue) CycleRepeat() RepeatMode {
	switch q.repeat {
	case RepeatOff:
		q.repeat = RepeatAll
	case RepeatAll:
		q.repeat = RepeatOne
	case RepeatOne:
		q.repeat = RepeatOff
	default:
		q.repeat = RepeatOff
	}
	return q.repeat
}

// Update applies fn to the item with the given ID. The original ID is
// preserved even if fn changes it. Unknown IDs return false.
func (q *Queue) Update(id uint64, fn func(*Item)) bool {
	for i := range q.items {
		if q.items[i].ID == id {
			if fn != nil {
				fn(&q.items[i])
			}
			q.items[i].ID = id
			return true
		}
	}
	return false
}

// Snapshot returns a copy of the queue state for persistence.
func (q *Queue) Snapshot() Snapshot {
	items := make([]Item, len(q.items))
	copy(items, q.items)
	return Snapshot{
		Items:   items,
		Current: q.current,
		Shuffle: q.shuffle,
		Repeat:  q.repeat,
	}
}

// Restore replaces the queue with a validated snapshot. Every restored item
// receives a fresh ID starting at 1, incoming IDs are ignored, and shuffle
// order is rebuilt when requested. Invalid snapshots return an error and leave
// the queue unchanged.
func (q *Queue) Restore(s Snapshot) error {
	if s.Repeat < RepeatOff || s.Repeat > RepeatOne {
		return fmt.Errorf("queue: invalid repeat mode %d", s.Repeat)
	}
	if s.Current < -1 || s.Current >= len(s.Items) {
		return fmt.Errorf("queue: current index %d out of range for %d items", s.Current, len(s.Items))
	}

	items := make([]Item, len(s.Items))
	for i, item := range s.Items {
		item.ID = uint64(i + 1)
		items[i] = item
	}

	q.items = items
	q.current = s.Current
	q.shuffle = s.Shuffle
	q.repeat = s.Repeat
	q.nextID = uint64(len(items))
	if q.shuffle {
		q.buildOrder()
	} else {
		q.order = nil
	}
	return nil
}

// rebuildOrder refreshes the shuffle order after a wholesale replacement.
func (q *Queue) rebuildOrder() {
	if q.shuffle {
		q.buildOrder()
	} else {
		q.order = nil
	}
}

// buildOrder assigns the current items to a random permutation of their IDs.
func (q *Queue) buildOrder() {
	if !q.shuffle || len(q.items) == 0 {
		q.order = nil
		return
	}
	q.ensureRand()
	q.order = make([]uint64, len(q.items))
	for i, item := range q.items {
		q.order[i] = item.ID
	}
	q.rng.Shuffle(len(q.order), func(i, j int) {
		q.order[i], q.order[j] = q.order[j], q.order[i]
	})
}

// ensureRand covers queues not created through New or newWithRand.
func (q *Queue) ensureRand() {
	if q.rng == nil {
		q.rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
}

// removeFromOrder deletes id from the shuffle order, if present.
func (q *Queue) removeFromOrder(id uint64) {
	for i, candidate := range q.order {
		if candidate == id {
			copy(q.order[i:], q.order[i+1:])
			q.order[len(q.order)-1] = 0
			q.order = q.order[:len(q.order)-1]
			return
		}
	}
}

// playbackOrder returns item IDs in playback order. Without shuffle this is
// slice order.
func (q *Queue) playbackOrder() []uint64 {
	if q.shuffle && len(q.order) > 0 {
		return q.order
	}
	order := make([]uint64, len(q.items))
	for i, item := range q.items {
		order[i] = item.ID
	}
	return order
}

// positionOf returns the position of id in order, or -1.
func (q *Queue) positionOf(id uint64, order []uint64) int {
	for i, candidate := range order {
		if candidate == id {
			return i
		}
	}
	return -1
}

// selectID makes the item with id current and returns it.
func (q *Queue) selectID(id uint64) (Item, bool) {
	for i := range q.items {
		if q.items[i].ID == id {
			q.current = i
			return q.items[i], true
		}
	}
	return Item{}, false
}
