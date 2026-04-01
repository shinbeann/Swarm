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
	robot *Robot
	send  SendFunc

	stopCh chan struct{}
	wg     sync.WaitGroup
}

func NewGossipEngine(robot *Robot, send SendFunc) *GossipEngine {
	return &GossipEngine{
		robot:  robot,
		send:   send,
		stopCh: make(chan struct{}),
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
	active := ge.robot.activeNeighbours()
	if len(active) == 0 {
		return
	}

	target := active[rand.Intn(len(active))]
	msg := &GossipMessage{
		SenderID:  RobotID(ge.robot.ID),
		Timestamp: ge.robot.Clock.Tick(),
		Entries:   ge.robot.store.GetAll(),
	}

	if err := ge.send(target, msg); err != nil {
		log.Printf("[gossip engine] %s failed to send to %s: %v", ge.robot.ID, target, err)
	}
}

func (ge *GossipEngine) OnReceive(msg *GossipMessage) {
	ge.robot.Clock.Update(msg.Timestamp)
	for _, entry := range msg.Entries {
		ge.robot.store.Add(entry.ID, entry.Type, entry.Location, msg.SenderID, msg.Timestamp)
	}
}

func (ge *GossipEngine) RecordDiscovery(id LandmarkID, ltype LandmarkType, loc Location) {
	timestamp := ge.robot.Clock.Tick()
	ge.robot.store.Add(id, ltype, loc, RobotID(ge.robot.ID), timestamp)
	log.Printf("[discovery] %s found landmark %s at (%.1f, %.1f)", ge.robot.ID, id, loc.X, loc.Y)
}
