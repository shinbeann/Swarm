package main

type LamportClock struct {
	time int
}

func NewLamportClock() *LamportClock {
	return &LamportClock{time: 0}
}

func (c *LamportClock) Tick() {
	c.time++
}

func (c *LamportClock) Time() int {
	return c.time
}

func (c *LamportClock) Update(received int) {
	if received > c.time {
		c.time = received
	}
	c.time++
}
