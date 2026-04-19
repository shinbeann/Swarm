package main

import (
	"testing"
	"time"
)

// Objective: verify a direct neighbour is recorded as a one-hop route.
// Expected output: the route exists with hop count 1 and next hop equal to the neighbour.
func TestRoutingTableDirectNeighbour(t *testing.T) {
	rt := NewRoutingTable("A")

	rt.RecordDirectNeighbour("B")
	route, ok := rt.GetRoute("B")
	if !ok {
		t.Fatal("expected route to B")
	}
	if route.HopCount != 1 {
		t.Fatalf("expected 1 hop to B, got %d", route.HopCount)
	}
	if route.NextHop != "B" {
		t.Fatalf("expected next hop B, got %s", route.NextHop)
	}
}

// Objective: verify merged advertisements expand routes through the advertising neighbour.
// Expected output: B, C, and D are reachable with hop counts 1, 2, and 3 respectively via B.
func TestRoutingTableMergeAdvertisement(t *testing.T) {
	rt := NewRoutingTable("A")

	// B is a direct neighbour.
	rt.RecordDirectNeighbour("B")

	// B advertises: {C: 1, D: 2} — meaning B can reach C in 1 hop, D in 2 hops.
	rt.MergeAdvertisement("B", map[string]int{
		"B": 0, // Self-advertisement from B.
		"C": 1,
		"D": 2,
	})

	// A should now know: B=1hop, C=2hops(via B), D=3hops(via B).
	routeB, ok := rt.GetRoute("B")
	if !ok {
		t.Fatal("expected route to B")
	}
	if routeB.HopCount != 1 {
		t.Fatalf("B hop count: expected 1, got %d", routeB.HopCount)
	}

	routeC, ok := rt.GetRoute("C")
	if !ok {
		t.Fatal("expected route to C")
	}
	if routeC.HopCount != 2 {
		t.Fatalf("C hop count: expected 2, got %d", routeC.HopCount)
	}
	if routeC.NextHop != "B" {
		t.Fatalf("C next hop: expected B, got %s", routeC.NextHop)
	}

	routeD, ok := rt.GetRoute("D")
	if !ok {
		t.Fatal("expected route to D")
	}
	if routeD.HopCount != 3 {
		t.Fatalf("D hop count: expected 3, got %d", routeD.HopCount)
	}
	if routeD.NextHop != "B" {
		t.Fatalf("D next hop: expected B, got %s", routeD.NextHop)
	}
}

// Objective: verify a shorter advertised path replaces a longer route.
// Expected output: D is first reached via B in 3 hops, then updated to 2 hops via E.
func TestRoutingTableShorterPathWins(t *testing.T) {
	rt := NewRoutingTable("A")

	// B advertises D reachable in 2 hops (A->B->?->D = 3 hops for A).
	rt.RecordDirectNeighbour("B")
	rt.MergeAdvertisement("B", map[string]int{"D": 2})

	routeD, ok := rt.GetRoute("D")
	if !ok {
		t.Fatal("expected route to D via B")
	}
	if routeD.HopCount != 3 || routeD.NextHop != "B" {
		t.Fatalf("D via B: expected 3 hops via B, got %d hops via %s", routeD.HopCount, routeD.NextHop)
	}

	// E advertises D reachable in 1 hop (A->E->D = 2 hops for A). Shorter path.
	rt.RecordDirectNeighbour("E")
	rt.MergeAdvertisement("E", map[string]int{"D": 1})

	routeD, ok = rt.GetRoute("D")
	if !ok {
		t.Fatal("expected route to D via E")
	}
	if routeD.HopCount != 2 {
		t.Fatalf("D via E: expected 2 hops, got %d", routeD.HopCount)
	}
	if routeD.NextHop != "E" {
		t.Fatalf("D via E: expected next hop E, got %s", routeD.NextHop)
	}
}

// Objective: verify expired routing entries are pruned.
// Expected output: the route table drops all aged entries after pruning.
func TestRoutingTablePruneExpired(t *testing.T) {
	rt := NewRoutingTable("A")

	rt.RecordDirectNeighbour("B")
	rt.MergeAdvertisement("B", map[string]int{"C": 1})

	if rt.Size() != 2 {
		t.Fatalf("expected 2 routes, got %d", rt.Size())
	}

	// Manually age out routes.
	rt.mu.Lock()
	for _, r := range rt.routes {
		r.LastUpdated = time.Now().Add(-20 * time.Second)
	}
	rt.mu.Unlock()

	rt.PruneExpired(10 * time.Second)

	if rt.Size() != 0 {
		t.Fatalf("expected 0 routes after pruning, got %d", rt.Size())
	}
}

// Objective: verify advertisements include self and all reachable peers with the expected hop counts.
// Expected output: the advertisement reports A=0, B=1, and C=2.
func TestRoutingTableBuildAdvertisement(t *testing.T) {
	rt := NewRoutingTable("A")
	rt.RecordDirectNeighbour("B")
	rt.MergeAdvertisement("B", map[string]int{"C": 1})

	advert := rt.BuildAdvertisement()

	// Should include: A=0, B=1, C=2
	if advert["A"] != 0 {
		t.Fatalf("expected A=0 in advertisement, got %d", advert["A"])
	}
	if advert["B"] != 1 {
		t.Fatalf("expected B=1 in advertisement, got %d", advert["B"])
	}
	if advert["C"] != 2 {
		t.Fatalf("expected C=2 in advertisement, got %d", advert["C"])
	}
}

// Objective: verify self references are ignored in routing updates.
// Expected output: recording or advertising self does not create a route to A.
func TestRoutingTableSelfIDIgnored(t *testing.T) {
	rt := NewRoutingTable("A")

	// Recording self as neighbour should be no-op.
	rt.RecordDirectNeighbour("A")
	if rt.Size() != 0 {
		t.Fatalf("expected 0 routes after recording self, got %d", rt.Size())
	}

	// Merging self's advertisement should not create a route to self.
	rt.MergeAdvertisement("B", map[string]int{"A": 1})
	if _, ok := rt.GetRoute("A"); ok {
		t.Fatal("should not have a route to self")
	}
}

// Objective: verify the full reachable set includes direct and indirect peers.
// Expected output: the reachable set contains B, C, D, and E.
func TestRoutingTableGetAllReachable(t *testing.T) {
	rt := NewRoutingTable("A")
	rt.RecordDirectNeighbour("B")
	rt.RecordDirectNeighbour("C")
	rt.MergeAdvertisement("B", map[string]int{"D": 1, "E": 2})

	all := rt.GetAllReachable()
	// Should have: B, C, D, E
	if len(all) != 4 {
		t.Fatalf("expected 4 reachable, got %d", len(all))
	}
	for _, id := range []RobotID{"B", "C", "D", "E"} {
		if _, ok := all[id]; !ok {
			t.Fatalf("expected %s in reachable set", id)
		}
	}
}
