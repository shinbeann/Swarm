package main

import (
	"log"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// GossipMessage is exchanged between neighbouring robots.
type GossipMessage struct {
	SenderID  RobotID
	Timestamp int
	Entries   []*LandmarkEntry
	Routes    map[string]int `json:"routes,omitempty"` // Routing advertisement: destination → hop count
}

type SendFunc func(to RobotID, msg *GossipMessage) error

// GossipEngine manages local gossip protocol for one robot.
type GossipEngine struct {
	robot *Robot
	send  SendFunc

	mu           sync.Mutex
	lastSyncTime map[RobotID]int

	stopCh chan struct{}
	wg     sync.WaitGroup
}

const (
	gossipFanout = 3
)

func NewGossipEngine(robot *Robot, send SendFunc) *GossipEngine {
	return &GossipEngine{
		robot:        robot,
		send:         send,
		lastSyncTime: make(map[RobotID]int),
		stopCh:       make(chan struct{}),
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
	if ge.robot.isPaused() {
		return
	}

	active := ge.robot.activeNeighbours()
	eligible := make([]RobotID, 0, len(active))
	for _, peerID := range active {
		// build eligibility list of active neighbours with known network conditions
		if _, ok := ge.robot.networkCondition(peerID); ok {
			eligible = append(eligible, peerID)
		}
	}
	if len(eligible) == 0 {
		return
	}

	targets := randomFanoutPeers(eligible, gossipFanout)

	// Prune expired routes before building the advertisement.
	ge.robot.routingTable.PruneExpired(routeExpiryTimeout)
	routes := ge.robot.routingTable.BuildAdvertisement()

	for _, target := range targets {
		cond, ok := ge.robot.networkCondition(target)
		if !ok {
			continue
		}

		delta := ge.deltaForPeer(target, cond.GetBandwidth())
		if len(delta) == 0 {
			log.Printf("[gossip] %s skipping %s - no new entries since ts=%d",
				ge.robot.ID, target, ge.peerSyncTime(target))
			continue
		}

		sentAt := ge.robot.Clock.Tick()
		msg := &GossipMessage{
			SenderID:  RobotID(ge.robot.ID),
			Timestamp: sentAt,
			Entries:   delta,
			Routes:    routes,
		}
		log.Printf("[gossip] %s -> %s delta=%d entries=%v",
			ge.robot.ID, target, len(delta), entryIDs(delta))

		if err := ge.send(target, msg); err != nil {
			log.Printf("[gossip engine] %s failed to send to %s: %v", ge.robot.ID, target, err)
			continue
		}
		ge.setPeerSyncTime(target, sentAt)
	}
}

func randomFanoutPeers(peers []RobotID, fanout int) []RobotID {
	if len(peers) == 0 || fanout <= 0 {
		return nil
	}

	shuffled := append([]RobotID(nil), peers...)
	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	if len(shuffled) > fanout {
		shuffled = shuffled[:fanout]
	}
	return shuffled
}

func (ge *GossipEngine) deltaForPeer(peerID RobotID, bandwidth float64) []*LandmarkEntry {
	lastSync := ge.peerSyncTime(peerID)
	allEntries := ge.robot.store.GetAll()

	delta := make([]*LandmarkEntry, 0, len(allEntries))
	for _, entry := range allEntries {
		if entryTimestamp(entry) > lastSync {
			delta = append(delta, entry)
		}
	}
	if len(delta) == 0 {
		return nil
	}

	sort.Slice(delta, func(i, j int) bool {
		left := delta[i]
		right := delta[j]

		leftCasualty := left.Type == LandmarkCasualty
		rightCasualty := right.Type == LandmarkCasualty
		if leftCasualty != rightCasualty {
			return leftCasualty
		}

		leftTS := entryTimestamp(left)
		rightTS := entryTimestamp(right)
		if leftTS != rightTS {
			return leftTS > rightTS
		}

		return left.ID < right.ID
	})

	totalCandidates := len(delta)
	limit := bandwidthEntryCap(bandwidth)
	sendCount := totalCandidates
	dropped := []*LandmarkEntry{}
	if len(delta) > limit {
		dropped = append(dropped, delta[limit:]...)
		delta = delta[:limit]
		sendCount = limit
	}
	log.Printf("[gossip] delta for %s: total=%d cap=%d sending=%d",
		peerID, totalCandidates, limit, len(delta))
	for _, e := range delta[:sendCount] {
		log.Printf("[gossip] included in delta: %s (type=%s priority=%d)",
			e.ID, e.Type, entryPriority(e))
	}
	for _, e := range dropped {
		log.Printf("[gossip] dropped from delta: %s (type=%s priority=%d)",
			e.ID, e.Type, entryPriority(e))
	}
	return delta
}

func entryTimestamp(entry *LandmarkEntry) int {
	maxTimestamp := 0
	for _, ts := range entry.Reporters {
		if ts > maxTimestamp {
			maxTimestamp = ts
		}
	}
	return maxTimestamp
}

func bandwidthEntryCap(bandwidth float64) int {
	if bandwidth <= 0 {
		return 1
	}
	limit := int(math.Floor(bandwidth))
	if limit < 1 {
		return 1
	}
	return limit
}

func entryIDs(entries []*LandmarkEntry) []LandmarkID {
	ids := make([]LandmarkID, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return ids
}

func entryPriority(entry *LandmarkEntry) int {
	if entry != nil && entry.Type == LandmarkCasualty {
		return 0
	}
	return 1
}

func (ge *GossipEngine) peerSyncTime(peerID RobotID) int {
	ge.mu.Lock()
	defer ge.mu.Unlock()
	return ge.lastSyncTime[peerID]
}

func (ge *GossipEngine) setPeerSyncTime(peerID RobotID, timestamp int) {
	ge.mu.Lock()
	defer ge.mu.Unlock()
	ge.lastSyncTime[peerID] = timestamp
}

func (ge *GossipEngine) OnReceive(msg *GossipMessage) {
	if ge.robot.isPaused() {
		return
	}

	ge.robot.Clock.Update(msg.Timestamp)
	for _, entry := range msg.Entries {
		ge.robot.store.MergeSnapshot(entry)
	}

	// Record sender as a direct 1-hop neighbour in the routing table.
	ge.robot.routingTable.RecordDirectNeighbour(msg.SenderID)

	// Merge sender's routing advertisement via distance-vector (Bellman-Ford).
	if msg.Routes != nil {
		ge.robot.routingTable.MergeAdvertisement(msg.SenderID, msg.Routes)
	}
}

func (ge *GossipEngine) RecordDiscovery(id LandmarkID, ltype LandmarkType, loc Location) {
	if ge.robot.isPaused() {
		return
	}

	timestamp := ge.robot.Clock.Tick()
	ge.robot.store.Add(id, ltype, loc, RobotID(ge.robot.ID), timestamp)
	log.Printf("[discovery] %s found landmark %s at (%.1f, %.1f)", ge.robot.ID, id, loc.X, loc.Y)
}
