package main

import (
	"sync"
	"testing"
)

func TestLamportClockStartsAtZero(t *testing.T) {
	c := NewLamportClock()
	if got := c.Time(); got != 0 {
		t.Fatalf("expected initial time 0, got %d", got)
	}
}

func TestLamportClockTickIncrements(t *testing.T) {
	c := NewLamportClock()

	if got := c.Tick(); got != 1 {
		t.Fatalf("expected Tick() = 1, got %d", got)
	}
	if got := c.Tick(); got != 2 {
		t.Fatalf("expected Tick() = 2, got %d", got)
	}
	if got := c.Time(); got != 2 {
		t.Fatalf("expected Time() = 2, got %d", got)
	}
}

func TestLamportClockUpdateWithGreaterTime(t *testing.T) {
	c := NewLamportClock()

	c.Tick() // local = 1

	got := c.Update(5) // max(1,5)+1 = 6
	if got != 6 {
		t.Fatalf("expected Update(5) = 6, got %d", got)
	}

	if got := c.Time(); got != 6 {
		t.Fatalf("expected Time() = 6, got %d", got)
	}
}

func TestLamportClockUpdateWithSmallerTime(t *testing.T) {
	c := NewLamportClock()

	c.Tick() // 1
	c.Tick() // 2
	c.Tick() // 3

	got := c.Update(1) // max(3,1)+1 = 4
	if got != 4 {
		t.Fatalf("expected Update(1) = 4, got %d", got)
	}

	if got := c.Time(); got != 4 {
		t.Fatalf("expected Time() = 4, got %d", got)
	}
}

func TestLamportClockUpdateWithEqualTime(t *testing.T) {
	c := NewLamportClock()

	c.Tick() // 1
	c.Tick() // 2

	got := c.Update(2) // max(2,2)+1 = 3
	if got != 3 {
		t.Fatalf("expected Update(2) = 3, got %d", got)
	}
}

func TestLamportClockConcurrentTicks(t *testing.T) {
	c := NewLamportClock()

	const n = 1000
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			c.Tick()
		}()
	}

	wg.Wait()

	if got := c.Time(); got != n {
		t.Fatalf("expected time %d after %d concurrent ticks, got %d", n, n, got)
	}
}

func TestLamportClockConcurrentMixedOperations(t *testing.T) {
	c := NewLamportClock()

	const ticks = 500
	const updates = 500

	var wg sync.WaitGroup
	wg.Add(ticks + updates)

	for i := 0; i < ticks; i++ {
		go func() {
			defer wg.Done()
			c.Tick()
		}()
	}

	for i := 0; i < updates; i++ {
		received := i % 20
		go func(r int) {
			defer wg.Done()
			c.Update(r)
		}(received)
	}

	wg.Wait()

	// We cannot predict exact final value due to interleaving,
	// but it must be at least ticks+updates because every operation increments once.
	minExpected := ticks + updates
	if got := c.Time(); got < minExpected {
		t.Fatalf("expected final time >= %d, got %d", minExpected, got)
	}
}

func TestLamportClockMonotonicSequence(t *testing.T) {
	c := NewLamportClock()

	last := c.Time()
	ops := []func() int{
		func() int { return c.Tick() },
		func() int { return c.Update(0) },
		func() int { return c.Update(10) },
		func() int { return c.Tick() },
		func() int { return c.Update(3) },
	}

	for i, op := range ops {
		got := op()
		if got <= last {
			t.Fatalf("operation %d produced non-monotonic time: last=%d got=%d", i, last, got)
		}
		last = got
	}
}
