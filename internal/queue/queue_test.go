package queue

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"slices"
	"testing"
	"time"
)

func newTestQueue(seed int64) *Queue {
	return newWithRand(rand.New(rand.NewSource(seed)))
}

func qi(path string) Item {
	return Item{Path: path, Title: path}
}

func qitems(paths ...string) []Item {
	items := make([]Item, len(paths))
	for i, path := range paths {
		items[i] = qi(path)
	}
	return items
}

func snapshotPaths(s Snapshot) []string {
	out := make([]string, len(s.Items))
	for i, item := range s.Items {
		out[i] = item.Path
	}
	return out
}

func queueIDs(q *Queue) []uint64 {
	s := q.Snapshot()
	out := make([]uint64, len(s.Items))
	for i, item := range s.Items {
		out[i] = item.ID
	}
	return out
}

func indexOfPath(q *Queue, path string) int {
	for i, item := range q.Snapshot().Items {
		if item.Path == path {
			return i
		}
	}
	return -1
}

func indexOfID(q *Queue, id uint64) int {
	for i, item := range q.Snapshot().Items {
		if item.ID == id {
			return i
		}
	}
	return -1
}

func requirePaths(t *testing.T, q *Queue, want ...string) {
	t.Helper()
	got := snapshotPaths(q.Snapshot())
	if !slices.Equal(got, want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
}

func requireIDs(t *testing.T, q *Queue, want ...uint64) {
	t.Helper()
	got := queueIDs(q)
	if !slices.Equal(got, want) {
		t.Fatalf("IDs = %v, want %v", got, want)
	}
}

func requireCurrent(t *testing.T, q *Queue, want string) {
	t.Helper()
	item, ok := q.Current()
	if !ok {
		t.Fatalf("Current() not ok, want %q", want)
	}
	if item.Path != want {
		t.Fatalf("current path = %q, want %q", item.Path, want)
	}
}

func requireNoCurrent(t *testing.T, q *Queue) {
	t.Helper()
	if item, ok := q.Current(); ok {
		t.Fatalf("Current() = %+v, true; want false", item)
	}
	if got := q.Snapshot().Current; got != -1 {
		t.Fatalf("snapshot current = %d, want -1", got)
	}
}

// requirePermutation asserts that order contains every item ID exactly once and
// no other IDs.
func requirePermutation(t *testing.T, order []uint64, items []Item) {
	t.Helper()
	if len(order) != len(items) {
		t.Fatalf("order length = %d, want %d (%v)", len(order), len(items), order)
	}
	seen := make(map[uint64]bool, len(order))
	for _, id := range order {
		if seen[id] {
			t.Fatalf("order contains duplicate ID %d: %v", id, order)
		}
		seen[id] = true
	}
	for _, item := range items {
		if !seen[item.ID] {
			t.Fatalf("order %v missing item ID %d", order, item.ID)
		}
	}
}

func TestNewEmptyQueue(t *testing.T) {
	q := New()
	requireNoCurrent(t, q)

	if _, ok := q.Next(true); ok {
		t.Fatal("Next(true) on empty queue returned ok")
	}
	if _, ok := q.Next(false); ok {
		t.Fatal("Next(false) on empty queue returned ok")
	}
	if _, ok := q.Prev(); ok {
		t.Fatal("Prev() on empty queue returned ok")
	}

	snap := q.Snapshot()
	if len(snap.Items) != 0 {
		t.Fatalf("snapshot items = %v, want empty", snap.Items)
	}
	if snap.Current != -1 || snap.Shuffle || snap.Repeat != RepeatOff {
		t.Fatalf("snapshot = %+v, want empty defaults", snap)
	}

	q.Append()
	if !reflect.DeepEqual(q.Snapshot(), snap) {
		t.Fatalf("Append() changed empty queue: %+v", q.Snapshot())
	}
}

func TestAppendAssignsIDsAndKeepsCurrent(t *testing.T) {
	q := New()
	q.Append(qi("a"))

	item, ok := q.Current()
	if !ok || item.Path != "a" || item.ID != 1 {
		t.Fatalf("Current() = %+v, %v; want a with ID 1", item, ok)
	}

	// Incoming IDs are ignored; IDs are assigned by the queue.
	q.Append(Item{ID: 99, Path: "b"}, qi("c"))
	requirePaths(t, q, "a", "b", "c")
	requireIDs(t, q, 1, 2, 3)
	requireCurrent(t, q, "a")

	// Appending to a non-empty queue does not move the current item.
	if !q.SetCurrent(1) {
		t.Fatal("SetCurrent(1) failed")
	}
	q.Append(qi("d"))
	requireCurrent(t, q, "b")
	requireIDs(t, q, 1, 2, 3, 4)

	before := q.Snapshot()
	q.Append()
	if !reflect.DeepEqual(q.Snapshot(), before) {
		t.Fatal("Append() with no items changed the queue")
	}
}

func TestIDsAreNotReused(t *testing.T) {
	q := New()
	q.Append(qitems("a", "b", "c")...)
	q.Remove(1) // removes ID 2
	q.Append(qi("d"))

	requirePaths(t, q, "a", "c", "d")
	requireIDs(t, q, 1, 3, 4)
}

func TestReplace(t *testing.T) {
	q := newTestQueue(1)
	q.Append(qi("old"))

	q.Replace(Item{ID: 50, Path: "x"}, Item{ID: 51, Path: "y"})
	requirePaths(t, q, "x", "y")
	requireIDs(t, q, 2, 3) // IDs continue from the queue counter
	requireCurrent(t, q, "x")

	if !q.SetCurrent(1) {
		t.Fatal("SetCurrent(1) failed")
	}
	q.Replace(qi("z"))
	requirePaths(t, q, "z")
	requireIDs(t, q, 4)
	requireCurrent(t, q, "z")

	q.Replace()
	requirePaths(t, q)
	requireNoCurrent(t, q)

	q.Append(qi("after"))
	requirePaths(t, q, "after")
	requireIDs(t, q, 5)
	requireCurrent(t, q, "after")
}

func TestInsertAfterCurrent(t *testing.T) {
	t.Run("empty queue appends and sets first current", func(t *testing.T) {
		q := New()
		q.InsertAfterCurrent(qitems("a", "b")...)
		requirePaths(t, q, "a", "b")
		requireIDs(t, q, 1, 2)
		requireCurrent(t, q, "a")
	})

	t.Run("inserts after middle current", func(t *testing.T) {
		q := New()
		q.Append(qitems("a", "b", "c")...)
		if !q.SetCurrent(1) {
			t.Fatal("SetCurrent(1) failed")
		}
		q.InsertAfterCurrent(Item{ID: 99, Path: "x"}, qi("y"))
		requirePaths(t, q, "a", "b", "x", "y", "c")
		requireIDs(t, q, 1, 2, 4, 5, 3)
		requireCurrent(t, q, "b")
		if got := q.Snapshot().Current; got != 1 {
			t.Fatalf("current index = %d, want 1", got)
		}
	})

	t.Run("no current appends to end", func(t *testing.T) {
		q := newTestQueue(2)
		if err := q.Restore(Snapshot{Items: qitems("a", "b"), Current: -1}); err != nil {
			t.Fatalf("Restore: %v", err)
		}
		q.InsertAfterCurrent(qi("c"))
		requirePaths(t, q, "a", "b", "c")
		requireIDs(t, q, 1, 2, 3)
		requireNoCurrent(t, q)
	})

	t.Run("no items is a no-op", func(t *testing.T) {
		q := New()
		before := q.Snapshot()
		q.InsertAfterCurrent()
		if !reflect.DeepEqual(q.Snapshot(), before) {
			t.Fatal("InsertAfterCurrent() with no items changed the queue")
		}
	})
}

func TestRemoveInvalidIndex(t *testing.T) {
	q := newTestQueue(1)
	q.Append(qitems("a", "b")...)
	before := q.Snapshot()

	for _, index := range []int{-1, 2, 100} {
		item, ok := q.Remove(index)
		if ok {
			t.Fatalf("Remove(%d) returned ok", index)
		}
		if item != (Item{}) {
			t.Fatalf("Remove(%d) returned %+v, want zero Item", index, item)
		}
		if !reflect.DeepEqual(q.Snapshot(), before) {
			t.Fatalf("Remove(%d) changed the queue", index)
		}
	}
}

func TestRemoveBeforeCurrent(t *testing.T) {
	q := newTestQueue(1)
	q.Append(qitems("a", "b", "c", "d")...)
	q.SetCurrent(2) // c

	item, ok := q.Remove(0)
	if !ok || item.Path != "a" {
		t.Fatalf("Remove(0) = %+v, %v; want a, true", item, ok)
	}
	requirePaths(t, q, "b", "c", "d")
	requireIDs(t, q, 2, 3, 4)
	requireCurrent(t, q, "c")
	if got := q.Snapshot().Current; got != 1 {
		t.Fatalf("current index = %d, want 1", got)
	}
}

func TestRemoveAfterCurrent(t *testing.T) {
	q := newTestQueue(1)
	q.Append(qitems("a", "b", "c", "d")...)
	q.SetCurrent(1) // b

	item, ok := q.Remove(3)
	if !ok || item.Path != "d" {
		t.Fatalf("Remove(3) = %+v, %v; want d, true", item, ok)
	}
	requirePaths(t, q, "a", "b", "c")
	requireCurrent(t, q, "b")
	if got := q.Snapshot().Current; got != 1 {
		t.Fatalf("current index = %d, want 1", got)
	}
}

func TestRemoveCurrentSelectsNextItem(t *testing.T) {
	q := newTestQueue(1)
	q.Append(qitems("a", "b", "c")...)
	q.SetCurrent(1) // b

	item, ok := q.Remove(1)
	if !ok || item.Path != "b" {
		t.Fatalf("Remove(1) = %+v, %v; want b, true", item, ok)
	}
	requirePaths(t, q, "a", "c")
	requireIDs(t, q, 1, 3)
	requireCurrent(t, q, "c")
	if got := q.Snapshot().Current; got != 1 {
		t.Fatalf("current index = %d, want 1", got)
	}
}

func TestRemoveCurrentLastSelectsPrevious(t *testing.T) {
	q := newTestQueue(1)
	q.Append(qitems("a", "b", "c")...)
	q.SetCurrent(2) // c

	item, ok := q.Remove(2)
	if !ok || item.Path != "c" {
		t.Fatalf("Remove(2) = %+v, %v; want c, true", item, ok)
	}
	requirePaths(t, q, "a", "b")
	requireCurrent(t, q, "b")
	if got := q.Snapshot().Current; got != 1 {
		t.Fatalf("current index = %d, want 1", got)
	}
}

func TestRemoveCurrentFirstSelectsNewFirst(t *testing.T) {
	q := newTestQueue(1)
	q.Append(qitems("a", "b", "c")...)
	item, ok := q.Remove(0)
	if !ok || item.Path != "a" {
		t.Fatalf("Remove(0) = %+v, %v; want a, true", item, ok)
	}
	requirePaths(t, q, "b", "c")
	requireCurrent(t, q, "b")
	if got := q.Snapshot().Current; got != 0 {
		t.Fatalf("current index = %d, want 0", got)
	}
}

func TestRemoveOnlyItemClearsCurrent(t *testing.T) {
	q := newTestQueue(1)
	q.Append(qi("a"))

	item, ok := q.Remove(0)
	if !ok || item.Path != "a" {
		t.Fatalf("Remove(0) = %+v, %v; want a, true", item, ok)
	}
	requirePaths(t, q)
	requireNoCurrent(t, q)
}

func TestRemoveWithNoCurrentLeavesNoCurrent(t *testing.T) {
	q := newTestQueue(1)
	if err := q.Restore(Snapshot{Items: qitems("a", "b"), Current: -1}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, ok := q.Remove(0); !ok {
		t.Fatal("Remove(0) failed")
	}
	requirePaths(t, q, "b")
	requireNoCurrent(t, q)
}

func TestRemoveRepairsShuffleOrder(t *testing.T) {
	q := newTestQueue(3)
	q.Append(qitems("a", "b", "c", "d", "e")...)
	q.SetShuffle(true)

	removedID := q.order[2]
	index := indexOfID(q, removedID)
	removed, ok := q.Remove(index)
	if !ok || removed.ID != removedID {
		t.Fatalf("Remove(%d) = %+v, %v; want ID %d", index, removed, ok, removedID)
	}
	if len(q.order) != len(q.Snapshot().Items) {
		t.Fatalf("order length = %d, items = %d", len(q.order), len(q.Snapshot().Items))
	}
	if slices.Contains(q.order, removedID) {
		t.Fatalf("order %v still contains removed ID %d", q.order, removedID)
	}
	requirePermutation(t, q.order, q.Snapshot().Items)
}

func TestMoveValidation(t *testing.T) {
	q := newTestQueue(1)
	q.Append(qitems("a", "b", "c")...)
	q.SetCurrent(1)
	before := q.Snapshot()

	for _, tc := range [][2]int{
		{-1, 1},
		{3, 0},
		{0, 0},
		{0, -1},
		{0, 3},
		{2, 1},
	} {
		if q.Move(tc[0], tc[1]) {
			t.Fatalf("Move(%d, %d) returned true", tc[0], tc[1])
		}
		if !reflect.DeepEqual(q.Snapshot(), before) {
			t.Fatalf("Move(%d, %d) changed the queue", tc[0], tc[1])
		}
	}
}

func TestMoveCurrentTracksItem(t *testing.T) {
	t.Run("current moves down", func(t *testing.T) {
		q := newTestQueue(1)
		q.Append(qitems("a", "b", "c", "d")...)
		q.SetCurrent(1) // b
		if !q.Move(1, 2) {
			t.Fatal("Move(1, 2) failed")
		}
		requirePaths(t, q, "a", "c", "d", "b")
		requireCurrent(t, q, "b")
		if got := q.Snapshot().Current; got != 3 {
			t.Fatalf("current index = %d, want 3", got)
		}
	})

	t.Run("current moves up", func(t *testing.T) {
		q := newTestQueue(1)
		q.Append(qitems("a", "b", "c", "d")...)
		q.SetCurrent(2) // c
		if !q.Move(2, -2) {
			t.Fatal("Move(2, -2) failed")
		}
		requirePaths(t, q, "c", "a", "b", "d")
		requireCurrent(t, q, "c")
		if got := q.Snapshot().Current; got != 0 {
			t.Fatalf("current index = %d, want 0", got)
		}
	})

	t.Run("item moves from before current to after it", func(t *testing.T) {
		q := newTestQueue(1)
		q.Append(qitems("a", "b", "c", "d")...)
		q.SetCurrent(1) // b
		if !q.Move(0, 2) {
			t.Fatal("Move(0, 2) failed")
		}
		requirePaths(t, q, "b", "c", "a", "d")
		requireCurrent(t, q, "b")
		if got := q.Snapshot().Current; got != 0 {
			t.Fatalf("current index = %d, want 0", got)
		}
	})

	t.Run("item moves from after current to before it", func(t *testing.T) {
		q := newTestQueue(1)
		q.Append(qitems("a", "b", "c", "d")...)
		q.SetCurrent(1) // b
		if !q.Move(3, -2) {
			t.Fatal("Move(3, -2) failed")
		}
		requirePaths(t, q, "a", "d", "b", "c")
		requireCurrent(t, q, "b")
		if got := q.Snapshot().Current; got != 2 {
			t.Fatalf("current index = %d, want 2", got)
		}
	})

	t.Run("movement entirely after current leaves index alone", func(t *testing.T) {
		q := newTestQueue(1)
		q.Append(qitems("a", "b", "c", "d")...)
		q.SetCurrent(1) // b
		if !q.Move(2, 1) {
			t.Fatal("Move(2, 1) failed")
		}
		requirePaths(t, q, "a", "b", "d", "c")
		requireCurrent(t, q, "b")
		if got := q.Snapshot().Current; got != 1 {
			t.Fatalf("current index = %d, want 1", got)
		}
	})

	t.Run("movement entirely before current leaves index alone", func(t *testing.T) {
		q := newTestQueue(1)
		q.Append(qitems("a", "b", "c", "d")...)
		q.SetCurrent(2) // c
		if !q.Move(0, 1) {
			t.Fatal("Move(0, 1) failed")
		}
		requirePaths(t, q, "b", "a", "c", "d")
		requireCurrent(t, q, "c")
		if got := q.Snapshot().Current; got != 2 {
			t.Fatalf("current index = %d, want 2", got)
		}
	})

	t.Run("no current stays no current", func(t *testing.T) {
		q := newTestQueue(1)
		if err := q.Restore(Snapshot{Items: qitems("a", "b", "c", "d"), Current: -1}); err != nil {
			t.Fatalf("Restore: %v", err)
		}
		if !q.Move(0, 2) {
			t.Fatal("Move(0, 2) failed")
		}
		requirePaths(t, q, "b", "c", "a", "d")
		requireNoCurrent(t, q)
	})
}

func TestClearKeepsSettings(t *testing.T) {
	q := newTestQueue(1)
	q.Append(qitems("a", "b", "c")...)
	q.SetCurrent(1)
	q.SetShuffle(true)
	q.SetRepeat(RepeatOne)
	if len(q.order) == 0 {
		t.Fatal("shuffle order is empty before Clear")
	}

	q.Clear()
	requirePaths(t, q)
	requireNoCurrent(t, q)
	if !q.shuffle {
		t.Fatal("Clear reset the shuffle flag")
	}
	if q.repeat != RepeatOne {
		t.Fatalf("Clear reset repeat mode to %d", q.repeat)
	}
	if len(q.order) != 0 {
		t.Fatalf("Clear left shuffle order %v", q.order)
	}
	if _, ok := q.Next(true); ok {
		t.Fatal("Next(true) succeeded after Clear")
	}
	if _, ok := q.Prev(); ok {
		t.Fatal("Prev() succeeded after Clear")
	}

	q.Append(qi("d"))
	requireIDs(t, q, 4)
	requireCurrent(t, q, "d")
	if !slices.Equal(q.order, []uint64{4}) {
		t.Fatalf("order after append = %v, want [4]", q.order)
	}
}

func TestSetCurrentBounds(t *testing.T) {
	q := New()
	if q.SetCurrent(0) {
		t.Fatal("SetCurrent(0) on empty queue returned true")
	}

	q.Append(qitems("a", "b")...)
	before := q.Snapshot()
	for _, index := range []int{-1, 2, 10} {
		if q.SetCurrent(index) {
			t.Fatalf("SetCurrent(%d) returned true", index)
		}
		if !reflect.DeepEqual(q.Snapshot(), before) {
			t.Fatalf("SetCurrent(%d) changed the queue", index)
		}
	}
	if !q.SetCurrent(1) {
		t.Fatal("SetCurrent(1) failed")
	}
	requireCurrent(t, q, "b")
	if !q.SetCurrent(0) {
		t.Fatal("SetCurrent(0) failed")
	}
	requireCurrent(t, q, "a")
}

func TestNextPrevRepeatOff(t *testing.T) {
	q := newTestQueue(1)
	q.Append(qitems("a", "b", "c")...)

	item, ok := q.Next(false)
	if !ok || item.Path != "b" || item.ID != 2 {
		t.Fatalf("Next(false) = %+v, %v; want b ID 2", item, ok)
	}
	requireCurrent(t, q, "b")

	item, ok = q.Next(false)
	if !ok || item.Path != "c" {
		t.Fatalf("Next(false) = %+v, %v; want c", item, ok)
	}
	if got := q.Snapshot().Current; got != 2 {
		t.Fatalf("current index = %d, want 2", got)
	}

	if _, ok := q.Next(true); ok {
		t.Fatal("Next(true) at end with repeat off returned ok")
	}
	if _, ok := q.Next(false); ok {
		t.Fatal("Next(false) at end with repeat off returned ok")
	}
	if got := q.Snapshot().Current; got != 2 {
		t.Fatalf("failed Next moved current to %d", got)
	}

	item, ok = q.Prev()
	if !ok || item.Path != "b" {
		t.Fatalf("Prev() = %+v, %v; want b", item, ok)
	}
	item, ok = q.Prev()
	if !ok || item.Path != "a" {
		t.Fatalf("Prev() = %+v, %v; want a", item, ok)
	}
	if _, ok := q.Prev(); ok {
		t.Fatal("Prev() at start with repeat off returned ok")
	}
	if got := q.Snapshot().Current; got != 0 {
		t.Fatalf("failed Prev moved current to %d", got)
	}
}

func TestNextRepeatAll(t *testing.T) {
	q := newTestQueue(1)
	q.Append(qitems("a", "b", "c")...)
	q.SetRepeat(RepeatAll)
	q.SetCurrent(2) // c

	item, ok := q.Next(true)
	if !ok || item.Path != "a" {
		t.Fatalf("Next(true) at end = %+v, %v; want wrapped a", item, ok)
	}
	if got := q.Snapshot().Current; got != 0 {
		t.Fatalf("current index = %d, want 0", got)
	}

	item, ok = q.Next(false)
	if !ok || item.Path != "b" {
		t.Fatalf("Next(false) = %+v, %v; want b", item, ok)
	}

	item, ok = q.Prev()
	if !ok || item.Path != "a" {
		t.Fatalf("Prev() = %+v, %v; want a", item, ok)
	}
	item, ok = q.Prev()
	if !ok || item.Path != "c" {
		t.Fatalf("Prev() at start = %+v, %v; want wrapped c", item, ok)
	}
	if got := q.Snapshot().Current; got != 2 {
		t.Fatalf("current index = %d, want 2", got)
	}

	// Manual next wraps too.
	q.SetCurrent(2)
	item, ok = q.Next(false)
	if !ok || item.Path != "a" {
		t.Fatalf("manual Next at end = %+v, %v; want wrapped a", item, ok)
	}
}

func TestNextRepeatOne(t *testing.T) {
	q := newTestQueue(1)
	q.Append(qitems("a", "b", "c")...)
	q.SetCurrent(1) // b
	q.SetRepeat(RepeatOne)

	// Automatic advance repeats the current item.
	item, ok := q.Next(true)
	if !ok || item.Path != "b" || item.ID != 2 {
		t.Fatalf("Next(true) with repeat one = %+v, %v; want b", item, ok)
	}
	if got := q.Snapshot().Current; got != 1 {
		t.Fatalf("current index = %d, want 1", got)
	}

	// Manual advance ignores repeat one.
	item, ok = q.Next(false)
	if !ok || item.Path != "c" {
		t.Fatalf("Next(false) with repeat one = %+v, %v; want c", item, ok)
	}

	// At the end, automatic next repeats current instead of failing.
	item, ok = q.Next(true)
	if !ok || item.Path != "c" {
		t.Fatalf("Next(true) at end with repeat one = %+v, %v; want c", item, ok)
	}
	if _, ok := q.Next(false); ok {
		t.Fatal("manual Next at end with repeat one returned ok")
	}
	if got := q.Snapshot().Current; got != 2 {
		t.Fatalf("current index = %d, want 2", got)
	}

	// Previous behaves like repeat off with repeat one.
	item, ok = q.Prev()
	if !ok || item.Path != "b" {
		t.Fatalf("Prev() with repeat one = %+v, %v; want b", item, ok)
	}
	q.SetCurrent(0)
	if _, ok := q.Prev(); ok {
		t.Fatal("Prev() at start with repeat one returned ok")
	}

	// No current: automatic next starts at the first item even with repeat one.
	if err := q.Restore(Snapshot{Items: qitems("x", "y"), Current: -1, Repeat: RepeatOne}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	item, ok = q.Next(true)
	if !ok || item.Path != "x" {
		t.Fatalf("Next(true) with no current = %+v, %v; want x", item, ok)
	}
}

func TestNextPrevWithInvalidCurrent(t *testing.T) {
	newNoCurrent := func(t *testing.T) *Queue {
		t.Helper()
		q := newTestQueue(1)
		if err := q.Restore(Snapshot{Items: qitems("a", "b", "c"), Current: -1}); err != nil {
			t.Fatalf("Restore: %v", err)
		}
		return q
	}

	t.Run("next auto starts at first", func(t *testing.T) {
		q := newNoCurrent(t)
		item, ok := q.Next(true)
		if !ok || item.Path != "a" || item.ID != 1 {
			t.Fatalf("Next(true) = %+v, %v; want a", item, ok)
		}
		if got := q.Snapshot().Current; got != 0 {
			t.Fatalf("current index = %d, want 0", got)
		}
	})

	t.Run("next manual starts at first", func(t *testing.T) {
		q := newNoCurrent(t)
		item, ok := q.Next(false)
		if !ok || item.Path != "a" {
			t.Fatalf("Next(false) = %+v, %v; want a", item, ok)
		}
	})

	t.Run("prev starts at first", func(t *testing.T) {
		q := newNoCurrent(t)
		item, ok := q.Prev()
		if !ok || item.Path != "a" {
			t.Fatalf("Prev() = %+v, %v; want a", item, ok)
		}
		if got := q.Snapshot().Current; got != 0 {
			t.Fatalf("current index = %d, want 0", got)
		}
	})
}

func TestShuffleDeterminism(t *testing.T) {
	q1 := newTestQueue(7)
	q2 := newTestQueue(7)
	items := qitems("a", "b", "c", "d", "e", "f")
	q1.Append(items...)
	q2.Append(items...)

	q1.SetShuffle(true)
	q2.SetShuffle(true)
	if !q1.shuffle || !q2.shuffle {
		t.Fatal("SetShuffle(true) did not set the flag")
	}
	if !slices.Equal(q1.order, q2.order) {
		t.Fatalf("same seed produced different orders: %v vs %v", q1.order, q2.order)
	}

	want := []uint64{1, 2, 3, 4, 5, 6}
	r := rand.New(rand.NewSource(7))
	r.Shuffle(len(want), func(i, j int) { want[i], want[j] = want[j], want[i] })
	if !slices.Equal(q1.order, want) {
		t.Fatalf("order = %v, want deterministic permutation %v", q1.order, want)
	}
	requirePermutation(t, q1.order, q1.Snapshot().Items)

	// Enabling shuffle again must not reshuffle an already shuffled queue.
	before := slices.Clone(q1.order)
	q1.SetShuffle(true)
	if !slices.Equal(q1.order, before) {
		t.Fatalf("SetShuffle(true) reshuffled: %v -> %v", before, q1.order)
	}

	// Disabling clears the precomputed order.
	q1.SetShuffle(false)
	if q1.shuffle {
		t.Fatal("SetShuffle(false) left shuffle enabled")
	}
	if len(q1.order) != 0 {
		t.Fatalf("SetShuffle(false) left order %v", q1.order)
	}
}

func TestShufflePlaybackOrder(t *testing.T) {
	q := newTestQueue(11)
	q.Append(qitems("a", "b", "c", "d", "e")...)
	q.SetShuffle(true)
	order := slices.Clone(q.order)

	q.SetCurrent(indexOfID(q, order[0]))
	for i := 1; i < len(order); i++ {
		item, ok := q.Next(false)
		if !ok || item.ID != order[i] {
			t.Fatalf("Next(false) #%d = %+v, %v; want ID %d", i, item, ok, order[i])
		}
	}

	if _, ok := q.Next(false); ok {
		t.Fatal("Next(false) past end of shuffled order returned ok")
	}
	if got, _ := q.Current(); got.ID != order[len(order)-1] {
		t.Fatalf("failed Next changed current to ID %d", got.ID)
	}

	q.SetRepeat(RepeatAll)
	item, ok := q.Next(true)
	if !ok || item.ID != order[0] {
		t.Fatalf("Next(true) at shuffled end = %+v, %v; want ID %d", item, ok, order[0])
	}

	item, ok = q.Prev()
	if !ok || item.ID != order[len(order)-1] {
		t.Fatalf("Prev() at shuffled start = %+v, %v; want wrapped ID %d", item, ok, order[len(order)-1])
	}
	item, ok = q.Prev()
	if !ok || item.ID != order[len(order)-2] {
		t.Fatalf("Prev() = %+v, %v; want ID %d", item, ok, order[len(order)-2])
	}
}

func TestShuffleMutationRepair(t *testing.T) {
	q := newTestQueue(3)
	q.Append(qitems("a", "b", "c", "d", "e")...)
	q.SetShuffle(true)
	original := slices.Clone(q.order)

	// Append adds the new ID at the end without disturbing the existing order.
	q.Append(qi("f"))
	if len(q.order) != 6 {
		t.Fatalf("order length after append = %d, want 6", len(q.order))
	}
	if !slices.Equal(q.order[:5], original) || q.order[5] != 6 {
		t.Fatalf("order after append = %v, want %v + [6]", q.order, original)
	}
	requirePermutation(t, q.order, q.Snapshot().Items)

	// InsertAfterCurrent also adds to the end.
	q.InsertAfterCurrent(qi("g"))
	if len(q.order) != 7 || q.order[6] != 7 {
		t.Fatalf("order after insert = %v, want trailing ID 7", q.order)
	}
	requirePermutation(t, q.order, q.Snapshot().Items)

	// Remove drops the ID.
	q.Remove(indexOfPath(q, "c"))
	requirePermutation(t, q.order, q.Snapshot().Items)
	if slices.Contains(q.order, 3) {
		t.Fatalf("order %v still contains removed ID 3", q.order)
	}

	// Move leaves the order untouched because it stores IDs.
	moved := slices.Clone(q.order)
	if !q.Move(0, len(q.Snapshot().Items)-1) {
		t.Fatal("Move failed")
	}
	if !slices.Equal(q.order, moved) {
		t.Fatalf("Move changed shuffle order: %v -> %v", moved, q.order)
	}

	// Replace rebuilds a shuffled order for the new items.
	q.Replace(qi("x"), qi("y"))
	if !q.shuffle {
		t.Fatal("Replace disabled shuffle")
	}
	requirePermutation(t, q.order, q.Snapshot().Items)
	if !slices.Equal(queueIDs(q), []uint64{8, 9}) {
		t.Fatalf("IDs after replace = %v, want [8 9]", queueIDs(q))
	}

	// Clear empties the order but keeps shuffle enabled.
	q.Clear()
	if len(q.order) != 0 {
		t.Fatalf("order after Clear = %v, want empty", q.order)
	}
	if !q.shuffle {
		t.Fatal("Clear disabled shuffle")
	}
}

func TestUpdatePreservesID(t *testing.T) {
	q := newTestQueue(1)
	q.Append(qi("a"), qi("b"))

	if !q.Update(1, func(item *Item) {
		item.Path = "changed"
		item.Title = "new title"
		item.Artist = "artist"
		item.ID = 999 // must be ignored
	}) {
		t.Fatal("Update(1) returned false")
	}

	items := q.Snapshot().Items
	if items[0].ID != 1 {
		t.Fatalf("ID after update = %d, want 1", items[0].ID)
	}
	if items[0].Path != "changed" || items[0].Title != "new title" || items[0].Artist != "artist" {
		t.Fatalf("updated item = %+v", items[0])
	}
	if items[1] != (Item{ID: 2, Path: "b", Title: "b"}) {
		t.Fatalf("update changed unrelated item: %+v", items[1])
	}
	if q.order != nil {
		// shuffle is off, order should stay empty
		if len(q.order) != 0 {
			t.Fatalf("update changed order: %v", q.order)
		}
	}

	before := q.Snapshot()
	called := false
	if q.Update(999, func(item *Item) { called = true }) {
		t.Fatal("Update(999) returned true for unknown ID")
	}
	if called {
		t.Fatal("Update called fn for unknown ID")
	}
	if !reflect.DeepEqual(q.Snapshot(), before) {
		t.Fatal("unknown-ID Update changed the queue")
	}

	// IDs stay stable across removals, so updates by ID keep working.
	if _, ok := q.Remove(0); !ok {
		t.Fatal("Remove(0) failed")
	}
	if !q.Update(2, func(item *Item) { item.Album = "album" }) {
		t.Fatal("Update(2) after removal returned false")
	}
	if got := q.Snapshot().Items[0]; got.ID != 2 || got.Album != "album" {
		t.Fatalf("item after update = %+v, want ID 2 with album", got)
	}
}

func TestSnapshotIsCopy(t *testing.T) {
	q := newTestQueue(1)
	q.Append(qi("a"), qi("b"))
	q.SetCurrent(1)
	q.SetShuffle(true)
	q.SetRepeat(RepeatAll)

	snap := q.Snapshot()
	if !slices.Equal(snapshotPaths(snap), []string{"a", "b"}) {
		t.Fatalf("snapshot paths = %v", snapshotPaths(snap))
	}
	if snap.Current != 1 || !snap.Shuffle || snap.Repeat != RepeatAll {
		t.Fatalf("snapshot = %+v", snap)
	}
	if !slices.Equal(queueIDs(q), []uint64{1, 2}) {
		t.Fatalf("IDs = %v", queueIDs(q))
	}

	// Mutating the snapshot must not affect the queue.
	snap.Items[0].Path = "mutated"
	snap.Items[0].Title = "mutated"
	snap.Current = 0
	snap.Shuffle = false
	snap.Repeat = RepeatOff

	after := q.Snapshot()
	if !slices.Equal(snapshotPaths(after), []string{"a", "b"}) {
		t.Fatalf("queue changed through snapshot: %v", snapshotPaths(after))
	}
	if after.Items[0].Title != "a" {
		t.Fatalf("queue item title changed through snapshot: %q", after.Items[0].Title)
	}
	if after.Current != 1 || !after.Shuffle || after.Repeat != RepeatAll {
		t.Fatalf("queue fields changed through snapshot: %+v", after)
	}

	// The snapshot owns a distinct backing array.
	if len(after.Items) > 0 && &after.Items[0] == &snap.Items[0] {
		t.Fatal("Snapshot did not copy the item slice")
	}
}

func TestJSONTags(t *testing.T) {
	item := Item{
		ID:       5,
		Path:     "p",
		CuePath:  "c",
		CueTrack: 2,
		Start:    1500 * time.Millisecond,
		Duration: 2 * time.Second,
		Title:    "t",
		Artist:   "ar",
		Album:    "al",
		TrackNo:  3,
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("Marshal item: %v", err)
	}
	var itemMap map[string]any
	if err := json.Unmarshal(data, &itemMap); err != nil {
		t.Fatalf("Unmarshal item: %v", err)
	}
	for _, key := range []string{"id", "path", "cue_path", "cue_track", "start", "duration", "title", "artist", "album", "track_no"} {
		if _, ok := itemMap[key]; !ok {
			t.Fatalf("item JSON %s missing key %q", data, key)
		}
	}
	if got := itemMap["start"]; got != float64(int64(1500*time.Millisecond)) {
		t.Fatalf("start = %v, want integer nanoseconds", got)
	}
	if got := itemMap["duration"]; got != float64(int64(2*time.Second)) {
		t.Fatalf("duration = %v, want integer nanoseconds", got)
	}

	snap := Snapshot{Items: []Item{item}, Current: 0, Shuffle: true, Repeat: RepeatAll}
	data, err = json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal snapshot: %v", err)
	}
	var snapMap map[string]any
	if err := json.Unmarshal(data, &snapMap); err != nil {
		t.Fatalf("Unmarshal snapshot: %v", err)
	}
	for _, key := range []string{"items", "current", "shuffle", "repeat"} {
		if _, ok := snapMap[key]; !ok {
			t.Fatalf("snapshot JSON %s missing key %q", data, key)
		}
	}
}

func TestRestore(t *testing.T) {
	q := newTestQueue(9)
	q.Append(qitems("old1", "old2")...)
	q.SetCurrent(1)
	q.SetRepeat(RepeatAll)
	q.SetShuffle(true)

	snap := Snapshot{
		Items: []Item{
			{ID: 100, Path: "x", Title: "X"},
			{ID: 200, Path: "y", Title: "Y"},
		},
		Current: 1,
		Shuffle: true,
		Repeat:  RepeatOne,
	}
	if err := q.Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	requirePaths(t, q, "x", "y")
	requireIDs(t, q, 1, 2) // fresh IDs, incoming IDs ignored
	requireCurrent(t, q, "y")
	if q.repeat != RepeatOne || !q.shuffle {
		t.Fatalf("restore settings = repeat %d, shuffle %v", q.repeat, q.shuffle)
	}
	requirePermutation(t, q.order, q.Snapshot().Items)

	// The ID counter continues after the restored items.
	q.Append(qi("z"))
	requireIDs(t, q, 1, 2, 3)

	// Restore copies the incoming slice.
	snap.Items[0].Path = "changed"
	requirePaths(t, q, "x", "y", "z")

	// Empty snapshot is valid and resets the ID counter.
	empty := newTestQueue(1)
	empty.Append(qi("old"))
	if err := empty.Restore(Snapshot{Current: -1}); err != nil {
		t.Fatalf("Restore empty: %v", err)
	}
	requirePaths(t, empty)
	requireNoCurrent(t, empty)
	empty.Append(qi("new"))
	requireIDs(t, empty, 1)

	// Items with no current are valid.
	noCurrent := newTestQueue(1)
	if err := noCurrent.Restore(Snapshot{Items: qitems("a", "b"), Current: -1}); err != nil {
		t.Fatalf("Restore no current: %v", err)
	}
	requireNoCurrent(t, noCurrent)
	item, ok := noCurrent.Next(false)
	if !ok || item.Path != "a" {
		t.Fatalf("Next(false) after restore without current = %+v, %v; want a", item, ok)
	}

	// Shuffle false clears any previous order.
	plain := newTestQueue(1)
	plain.Append(qi("old"))
	plain.SetShuffle(true)
	if err := plain.Restore(Snapshot{Items: qitems("a", "b"), Current: 0}); err != nil {
		t.Fatalf("Restore plain: %v", err)
	}
	if plain.shuffle || len(plain.order) != 0 {
		t.Fatalf("restore without shuffle left shuffle=%v order=%v", plain.shuffle, plain.order)
	}
}

func TestRestoreValidationLeavesQueueUnchanged(t *testing.T) {
	q := newTestQueue(1)
	q.Append(qitems("a", "b")...)
	q.SetCurrent(1)
	q.SetRepeat(RepeatAll)
	q.SetShuffle(true)
	before := q.Snapshot()
	beforeOrder := slices.Clone(q.order)

	cases := []struct {
		name string
		snap Snapshot
	}{
		{"current below minus one", Snapshot{Items: qitems("x"), Current: -2}},
		{"current equal to length", Snapshot{Items: qitems("x", "y"), Current: 2}},
		{"current past length", Snapshot{Items: qitems("x", "y"), Current: 99}},
		{"current set on empty queue", Snapshot{Current: 0}},
		{"repeat too high", Snapshot{Items: qitems("x"), Current: 0, Repeat: RepeatMode(3)}},
		{"repeat negative", Snapshot{Items: qitems("x"), Current: 0, Repeat: RepeatMode(-1)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := q.Restore(tc.snap); err == nil {
				t.Fatal("Restore returned nil error for invalid snapshot")
			}
			if !reflect.DeepEqual(q.Snapshot(), before) {
				t.Fatalf("invalid Restore changed queue: %+v", q.Snapshot())
			}
			if !slices.Equal(q.order, beforeOrder) {
				t.Fatalf("invalid Restore changed order: %v -> %v", beforeOrder, q.order)
			}
		})
	}

	// The failed restores did not consume IDs.
	q.Append(qi("c"))
	requireIDs(t, q, 1, 2, 3)
}

func TestRepeatSettings(t *testing.T) {
	q := New()
	if got := q.Snapshot().Repeat; got != RepeatOff {
		t.Fatalf("default repeat = %d, want RepeatOff", got)
	}

	q.SetRepeat(RepeatAll)
	if got := q.Snapshot().Repeat; got != RepeatAll {
		t.Fatalf("repeat = %d, want RepeatAll", got)
	}
	q.SetRepeat(RepeatOne)
	if got := q.Snapshot().Repeat; got != RepeatOne {
		t.Fatalf("repeat = %d, want RepeatOne", got)
	}

	// Invalid modes are ignored.
	q.SetRepeat(RepeatMode(3))
	q.SetRepeat(RepeatMode(-1))
	q.SetRepeat(RepeatMode(100))
	if got := q.Snapshot().Repeat; got != RepeatOne {
		t.Fatalf("invalid SetRepeat changed mode to %d", got)
	}

	if got := q.CycleRepeat(); got != RepeatOff {
		t.Fatalf("CycleRepeat() = %d, want RepeatOff", got)
	}
	if got := q.CycleRepeat(); got != RepeatAll {
		t.Fatalf("CycleRepeat() = %d, want RepeatAll", got)
	}
	if got := q.CycleRepeat(); got != RepeatOne {
		t.Fatalf("CycleRepeat() = %d, want RepeatOne", got)
	}
	if got := q.CycleRepeat(); got != RepeatOff {
		t.Fatalf("CycleRepeat() = %d, want RepeatOff", got)
	}
}

func TestLen(t *testing.T) {
	q := newTestQueue(1)
	if got := q.Len(); got != 0 {
		t.Fatalf("empty Len() = %d, want 0", got)
	}
	q.Append(qitems("a", "b", "c")...)
	if got := q.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3", got)
	}
	q.Remove(1)
	if got := q.Len(); got != 2 {
		t.Fatalf("Len() after Remove = %d, want 2", got)
	}
	q.Clear()
	if got := q.Len(); got != 0 {
		t.Fatalf("Len() after Clear = %d, want 0", got)
	}
}
