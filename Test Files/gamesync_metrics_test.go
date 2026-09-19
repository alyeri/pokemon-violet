package main

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	commonpb "npln.nintendo.net/npln-practice/proto/common"
	"testing"
)

func TestVioletNATMetricsOwnership(t *testing.T) {
	s := gamesyncSession{UID: "local", UserSession: "userSessions/local", GameSession: "room"}
	peer := gamesyncSession{UID: "peer", GameSession: "room"}
	m := &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"lu": gamesyncStringValue("local"), "ru": gamesyncStringValue("peer"),
		"p2p": gamesyncStringValue("nn::pia::npln::NplnPlugin"), "rc": gamesyncIntegerValue(1),
		"re": {ValueType: &commonpb.Value_BooleanValue{BooleanValue: false}},
	}}
	f := &commonpb.MapValue{Fields: map[string]*commonpb.Value{"a": {ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: []*commonpb.Value{gamesyncMapValue(m)}}}}}}
	check := func() error {
		return writableDocument("docs/__mt/nat_traversal", f, s, nil, nil, map[string]gamesyncSession{"local": s, "peer": peer})
	}
	if err := check(); err != nil {
		t.Fatal(err)
	}
	m.Fields["lu"] = gamesyncStringValue("forged")
	if status.Code(check()) != codes.PermissionDenied {
		t.Fatal("accepted forged sender")
	}
	m.Fields["lu"] = gamesyncStringValue("local")
	peer.GameSession = "other"
	if status.Code(check()) != codes.PermissionDenied {
		t.Fatal("accepted foreign peer")
	}
	peer.GameSession = "room"
	m.Fields["rc"] = gamesyncStringValue("1")
	if status.Code(check()) != codes.InvalidArgument {
		t.Fatal("accepted invalid result type")
	}
}
