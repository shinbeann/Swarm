package main

import (
	"context"
	"fmt"
	"log"
	"time"

	pb "github.com/yihre/swarm-project/communications/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

const (
	defaultMeshTTL = 10
	meshPayloadMb  = 0.002 // Approximate Raft RPC payload wrapped in RouteMessage.
	meshRPCTimeout = 3 * time.Second
)

// MeshRouter handles multi-hop message forwarding using the Robot's routing table.
// It transparently routes serialized Raft RPCs through intermediate robots.
type MeshRouter struct {
	robot *Robot
}

func NewMeshRouter(robot *Robot) *MeshRouter {
	return &MeshRouter{robot: robot}
}

// SendMessage routes a message to dest via the mesh network.
// It looks up the next hop in the routing table, applies per-hop network constraints,
// and sends a RoutedMessageRequest to the next hop's PeerService.
// Returns the response payload from the destination, or an error.
func (mr *MeshRouter) SendMessage(dest RobotID, msgType string, payload []byte) ([]byte, error) {
	route, ok := mr.robot.routingTable.GetRoute(dest)
	if !ok {
		return nil, fmt.Errorf("no route to %s", dest)
	}

	nextHop := route.NextHop

	// Apply network constraints for the 1-hop link to NextHop.
	cond, hasCond := mr.robot.networkCondition(nextHop)
	if !hasCond {
		return nil, fmt.Errorf("no network condition for next hop %s", nextHop)
	}

	if !mr.robot.applyNetworkConstraints(
		string(nextHop),
		cond.GetLatency(),
		cond.GetBandwidth(),
		cond.GetReliability(),
		meshPayloadMb,
		"Mesh",
	) {
		return nil, fmt.Errorf("mesh packet dropped on hop to %s", nextHop)
	}

	// Dial the next hop's PeerService.
	peerAddr := string(nextHop) + ":50052"
	conn, err := grpc.NewClient(peerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial next hop %s: %w", nextHop, err)
	}
	defer conn.Close()

	peerClient := pb.NewPeerServiceClient(conn)
	rpcCtx, rpcCancel := context.WithTimeout(context.Background(), meshRPCTimeout)
	defer rpcCancel()

	resp, err := peerClient.RouteMessage(rpcCtx, &pb.RoutedMessageRequest{
		SourceId:      mr.robot.ID,
		DestinationId: string(dest),
		MessageType:   msgType,
		Payload:       payload,
		Ttl:           defaultMeshTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("route message to %s via %s: %w", dest, nextHop, err)
	}

	if !resp.GetDelivered() {
		return nil, fmt.Errorf("message to %s not delivered (via %s)", dest, nextHop)
	}

	log.Printf("[Mesh Send] %s -> %s (%s) via %s hops=%d",
		mr.robot.ID, dest, msgType, nextHop, route.HopCount)

	return resp.GetResponsePayload(), nil
}

// HandleRouteMessage processes an incoming routed message.
// If this robot is the destination, it dispatches the inner RPC.
// Otherwise, it decrements TTL and forwards to the next hop.
func (mr *MeshRouter) HandleRouteMessage(ctx context.Context, req *pb.RoutedMessageRequest) *pb.RoutedMessageResponse {
	destID := req.GetDestinationId()

	// Are we the destination?
	if destID == mr.robot.ID {
		return mr.dispatchLocal(req)
	}

	// TTL check.
	ttl := req.GetTtl()
	if ttl <= 1 {
		log.Printf("[Mesh Forward] %s dropping message for %s (TTL expired)", mr.robot.ID, destID)
		return &pb.RoutedMessageResponse{Delivered: false}
	}

	// Look up next hop.
	route, ok := mr.robot.routingTable.GetRoute(RobotID(destID))
	if !ok {
		log.Printf("[Mesh Forward] %s has no route to %s", mr.robot.ID, destID)
		return &pb.RoutedMessageResponse{Delivered: false}
	}

	nextHop := route.NextHop

	// Apply network constraints for the hop.
	cond, hasCond := mr.robot.networkCondition(nextHop)
	if !hasCond {
		return &pb.RoutedMessageResponse{Delivered: false}
	}
	if !mr.robot.applyNetworkConstraints(
		string(nextHop),
		cond.GetLatency(),
		cond.GetBandwidth(),
		cond.GetReliability(),
		meshPayloadMb,
		"Mesh",
	) {
		return &pb.RoutedMessageResponse{Delivered: false}
	}

	// Forward.
	peerAddr := string(nextHop) + ":50052"
	conn, err := grpc.NewClient(peerAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return &pb.RoutedMessageResponse{Delivered: false}
	}
	defer conn.Close()

	peerClient := pb.NewPeerServiceClient(conn)
	rpcCtx, rpcCancel := context.WithTimeout(context.Background(), meshRPCTimeout)
	defer rpcCancel()

	forwardReq := &pb.RoutedMessageRequest{
		SourceId:      req.GetSourceId(),
		DestinationId: destID,
		MessageType:   req.GetMessageType(),
		Payload:       req.GetPayload(),
		Ttl:           ttl - 1,
	}

	resp, err := peerClient.RouteMessage(rpcCtx, forwardReq)
	if err != nil {
		log.Printf("[Mesh Forward] %s failed to forward to %s via %s: %v", mr.robot.ID, destID, nextHop, err)
		return &pb.RoutedMessageResponse{Delivered: false}
	}

	log.Printf("[Mesh Forward] %s forwarded %s -> %s via %s", mr.robot.ID, req.GetSourceId(), destID, nextHop)
	return resp
}

// dispatchLocal handles a routed message that has arrived at its destination.
// It deserializes the inner RPC, dispatches to the appropriate handler, and returns the response.
func (mr *MeshRouter) dispatchLocal(req *pb.RoutedMessageRequest) *pb.RoutedMessageResponse {
	switch req.GetMessageType() {
	case "RequestVote":
		var voteReq pb.VoteRequest
		if err := proto.Unmarshal(req.GetPayload(), &voteReq); err != nil {
			log.Printf("[Mesh Local] %s failed to unmarshal VoteRequest: %v", mr.robot.ID, err)
			return &pb.RoutedMessageResponse{Delivered: false}
		}
		resp := mr.robot.HandleRequestVote(&voteReq)
		respBytes, err := proto.Marshal(resp)
		if err != nil {
			return &pb.RoutedMessageResponse{Delivered: false}
		}
		return &pb.RoutedMessageResponse{Delivered: true, ResponsePayload: respBytes}

	case "AppendEntries":
		var appendReq pb.AppendEntriesRequest
		if err := proto.Unmarshal(req.GetPayload(), &appendReq); err != nil {
			log.Printf("[Mesh Local] %s failed to unmarshal AppendEntriesRequest: %v", mr.robot.ID, err)
			return &pb.RoutedMessageResponse{Delivered: false}
		}
		resp := mr.robot.HandleAppendEntries(&appendReq)
		respBytes, err := proto.Marshal(resp)
		if err != nil {
			return &pb.RoutedMessageResponse{Delivered: false}
		}
		return &pb.RoutedMessageResponse{Delivered: true, ResponsePayload: respBytes}

	default:
		log.Printf("[Mesh Local] %s unknown message type: %s", mr.robot.ID, req.GetMessageType())
		return &pb.RoutedMessageResponse{Delivered: false}
	}
}
