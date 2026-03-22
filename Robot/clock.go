package main

import "sync"

type LamportClock struct {
	mu   sync.Mutex
	time int
}

func NewLamportClock() *LamportClock {
	return &LamportClock{time: 0}
}

func (c *LamportClock) Tick() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.time++
	return c.time
}

func (c *LamportClock) Time() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.time
}

func (c *LamportClock) Update(received int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if received > c.time {
		c.time = received
	}
	c.time++
	return c.time
}
