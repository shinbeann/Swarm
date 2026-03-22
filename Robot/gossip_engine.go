package main

import (
	"log"
	"math/rand"
	"sync"
	"time"
)

// GossipMessage is exchanged between neighbouring robots.
type GossipMessage struct {
	SenderID  RobotID
	Timestamp int
	Entries   []*LandmarkEntry
}

type SendFunc func(to RobotID, msg *GossipMessage) error

// GossipEngine manages local gossip protocol for one robot.
type GossipEngine struct {
	id         RobotID
	clock      *LamportClock
	store      *KnowledgeStore
	neighbours *NeighbourRegistry
	send       SendFunc

	stopCh chan struct{}
	wg     sync.WaitGroup
}

func NewGossipEngine(id RobotID, clock *LamportClock, store *KnowledgeStore, neighbours *NeighbourRegistry, send SendFunc) *GossipEngine {
	return &GossipEngine{
		id:         id,
		clock:      clock,
		store:      store,
		neighbours: neighbours,
		send:       send,
		stopCh:     make(chan struct{}),
	}
}

func (ge *GossipEngine) Start() {
	ge.wg.Add(1)
	go ge.gossipLoop()
}

func (ge *GossipEngine) Stop() {
	close(ge.stopCh)
	ge.wg.Wait()
}

func (ge *GossipEngine) gossipLoop() {
	defer ge.wg.Done()
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ge.stopCh:
			return
		case <-ticker.C:
			ge.gossipOnce()
		}
	}
}

func (ge *GossipEngine) gossipOnce() {
	active := ge.neighbours.GetActive()
	if len(active) == 0 {
		return
	}

	target := active[rand.Intn(len(active))]
	msg := &GossipMessage{
		SenderID:  ge.id,
		Timestamp: ge.clock.Tick(),
		Entries:   ge.store.GetAll(),
	}

	if err := ge.send(target, msg); err != nil {
		log.Printf("[gossip] %s failed to send to %s: %v", ge.id, target, err)
	}
}

func (ge *GossipEngine) OnReceive(msg *GossipMessage) {
	ge.clock.Update(msg.Timestamp)
	for _, entry := range msg.Entries {
		ge.store.Add(entry.ID, entry.Type, entry.Location, msg.SenderID, msg.Timestamp)
	}
}

func (ge *GossipEngine) RecordDiscovery(id LandmarkID, ltype LandmarkType, loc Location) {
	timestamp := ge.clock.Tick()
	ge.store.Add(id, ltype, loc, ge.id, timestamp)
	log.Printf("[discovery] %s found landmark %s at (%.1f, %.1f)", ge.id, id, loc.X, loc.Y)
}
