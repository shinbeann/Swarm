package main

import (
	"sync"
	"time"
)

const (
	routeExpiryTimeout = 10 * time.Second
)

// Route represents a single entry in the distance-vector routing table.
type Route struct {
	NextHop     RobotID
	HopCount    int
	LastUpdated time.Time
}

// RoutingTable is a thread-safe distance-vector routing table.
// Each robot maintains one to discover multi-hop paths to every peer in the swarm.
type RoutingTable struct {
	mu     sync.RWMutex
	selfID RobotID
	routes map[RobotID]*Route
}

func NewRoutingTable(selfID RobotID) *RoutingTable {
	return &RoutingTable{
		selfID: selfID,
		routes: make(map[RobotID]*Route),
	}
}

// RecordDirectNeighbour adds or refreshes a 1-hop route to a direct physical neighbour.
func (rt *RoutingTable) RecordDirectNeighbour(id RobotID) {
	if id == rt.selfID {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()

	existing, ok := rt.routes[id]
	if !ok || existing.HopCount > 1 {
		// New route or we found a shorter (direct) path.
		rt.routes[id] = &Route{
			NextHop:     id,
			HopCount:    1,
			LastUpdated: time.Now(),
		}
	} else {
		// Already have a direct route, just refresh timestamp.
		existing.LastUpdated = time.Now()
	}
}

// MergeAdvertisement performs Bellman-Ford merge from a neighbour's routing advertisement.
// The advertisement is a map of destination → hopCount as known by the sender.
// If sender can reach dest in N hops, we can reach dest via sender in N+1 hops.
func (rt *RoutingTable) MergeAdvertisement(senderID RobotID, advert map[string]int) {
	if senderID == rt.selfID {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()

	now := time.Now()

	for destStr, hops := range advert {
		dest := RobotID(destStr)
		if dest == rt.selfID {
			continue // Don't route to ourselves.
		}

		candidateHops := hops + 1

		existing, ok := rt.routes[dest]
		if !ok {
			// No existing route — accept.
			rt.routes[dest] = &Route{
				NextHop:     senderID,
				HopCount:    candidateHops,
				LastUpdated: now,
			}
			continue
		}

		// Accept if shorter or if existing route goes through same next hop (refresh).
		if candidateHops < existing.HopCount {
			existing.NextHop = senderID
			existing.HopCount = candidateHops
			existing.LastUpdated = now
		} else if existing.NextHop == senderID {
			// Same path — refresh the timestamp and update hop count in case it changed.
			existing.HopCount = candidateHops
			existing.LastUpdated = now
		}
	}
}

// GetRoute returns the route to dest, if known.
func (rt *RoutingTable) GetRoute(dest RobotID) (Route, bool) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	r, ok := rt.routes[dest]
	if !ok {
		return Route{}, false
	}
	return *r, true
}

// GetAllReachable returns a snapshot of all reachable destinations.
func (rt *RoutingTable) GetAllReachable() map[RobotID]Route {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	snapshot := make(map[RobotID]Route, len(rt.routes))
	for id, r := range rt.routes {
		snapshot[id] = *r
	}
	return snapshot
}

// BuildAdvertisement exports the routing table as destination → hopCount
// for inclusion in gossip messages.
func (rt *RoutingTable) BuildAdvertisement() map[string]int {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	advert := make(map[string]int, len(rt.routes))
	for id, r := range rt.routes {
		advert[string(id)] = r.HopCount
	}
	// Include self as 0-hop so receivers know they can reach us.
	advert[string(rt.selfID)] = 0
	return advert
}

// PruneExpired removes routes that haven't been refreshed within maxAge.
func (rt *RoutingTable) PruneExpired(maxAge time.Duration) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	now := time.Now()
	for id, r := range rt.routes {
		if now.Sub(r.LastUpdated) > maxAge {
			delete(rt.routes, id)
		}
	}
}

// Size returns the number of entries in the routing table.
func (rt *RoutingTable) Size() int {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return len(rt.routes)
}
