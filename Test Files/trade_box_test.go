package main

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protowire"
)

func createTradeBoxWire(parent, id string, box timberTradeBox) []byte {
	out := appendStringField(nil, 1, parent)
	out = appendBytesField(out, 2, marshalTimberTradeBox(box))
	return appendStringField(out, 3, id)
}

func TestTimberTradeBoxWireContract(t *testing.T) {
	want := timberTradeBox{
		Name:      "tenants/current/users/current/tradeBoxes/box-a",
		State:     tradeBoxTraded,
		Timestamp: time.Unix(123456, 789).UTC(),
		Payload:   []byte{1, 2, 3, 4},
		Signature: []byte{5, 6, 7},
	}
	got, err := parseTimberTradeBox(marshalTimberTradeBox(want))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != want.Name || got.State != want.State || !got.Timestamp.Equal(want.Timestamp) || !bytes.Equal(got.Payload, want.Payload) || !bytes.Equal(got.Signature, want.Signature) {
		t.Fatalf("round trip mismatch: %#v", got)
	}
	req, err := parseCreateTradeBoxRequest(createTradeBoxWire("tenants/current/users/current", "box-a", timberTradeBox{Payload: want.Payload, Signature: want.Signature}))
	if err != nil {
		t.Fatal(err)
	}
	if req.Parent != "tenants/current/users/current" || req.TradeBoxID != "box-a" || !bytes.Equal(req.TradeBox.Payload, want.Payload) {
		t.Fatalf("request mismatch: %#v", req)
	}
	nameWire := appendStringField(nil, 1, want.Name)
	if name, err := parseProtoStringMessage(nameWire); err != nil || name != want.Name {
		t.Fatalf("name=%q err=%v", name, err)
	}
	response := marshalTrackTradeBoxResponse(&want, false, 0)
	number, typ, consumed := protowire.ConsumeTag(response)
	if consumed < 0 || number != 1 || typ != protowire.BytesType {
		t.Fatalf("track response does not start with TradeBox: %x", response)
	}
}

func requireMissingTrackResponse(t *testing.T, response []byte) {
	t.Helper()
	number, typ, n := protowire.ConsumeTag(response)
	if n < 0 || number != 2 || typ != protowire.VarintType {
		t.Fatalf("response has no missing marker: %x", response)
	}
	value, consumed := protowire.ConsumeVarint(response[n:])
	if consumed < 0 || value != 1 || n+consumed != len(response) {
		t.Fatalf("invalid missing marker: %x", response)
	}
}

func TestTimberTradeBoxPairsDifferentOwnersAndCrossesPayloads(t *testing.T) {
	server := newTradeBoxServer()
	first, err := server.createForOwner("u-first", createTradeBoxRequest{
		Parent: "tenants/current/users/current", TradeBoxID: "first",
		TradeBox: timberTradeBox{Payload: []byte("pokemon-a"), Signature: []byte("signature-a")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.State != tradeBoxTrading {
		t.Fatalf("first state=%d", first.State)
	}
	if _, err := server.createForOwner("u-first", createTradeBoxRequest{
		Parent: "tenants/current/users/current", TradeBoxID: "same-owner",
		TradeBox: timberTradeBox{Payload: []byte("pokemon-self"), Signature: []byte("signature-self")},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.createForOwner("u-second", createTradeBoxRequest{
		Parent: "tenants/current/users/current", TradeBoxID: "first",
		TradeBox: timberTradeBox{Payload: []byte("pokemon-b"), Signature: []byte("signature-b")},
	}); err != nil {
		t.Fatal(err)
	}

	server.mu.Lock()
	a := cloneTimberTradeBox(server.boxes[tradeBoxStorageKey("u-first", "first")].result)
	b := cloneTimberTradeBox(server.boxes[tradeBoxStorageKey("u-second", "first")].result)
	selfMatched := server.boxes[tradeBoxStorageKey("u-first", "same-owner")].matched
	server.mu.Unlock()
	if a.State != tradeBoxTraded || b.State != tradeBoxTraded || selfMatched {
		t.Fatalf("states a=%d b=%d self=%v", a.State, b.State, selfMatched)
	}
	if !bytes.Equal(a.Payload, []byte("pokemon-b")) || !bytes.Equal(a.Signature, []byte("signature-b")) {
		t.Fatal("first caller did not receive second payload")
	}
	if !bytes.Equal(b.Payload, []byte("pokemon-a")) || !bytes.Equal(b.Signature, []byte("signature-a")) {
		t.Fatal("second caller did not receive first payload")
	}
	if err := validateTradeBoxResourceName(a.Name); err != nil {
		t.Fatal(err)
	}
}

func TestTimberTradeBoxOwnershipAndCancellation(t *testing.T) {
	server := newTradeBoxServer()
	box, err := server.createForOwner("u-owner", createTradeBoxRequest{
		Parent: "tenants/current/users/current", TradeBoxID: "cancel-me",
		TradeBox: timberTradeBox{Payload: []byte{1}, Signature: []byte{2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.lookupOwned("u-other", box.Name); status.Code(err) != codes.NotFound {
		t.Fatalf("foreign lookup code=%s err=%v", status.Code(err), err)
	}
	if err := server.cancelForOwner("u-owner", box.Name, false); err != nil {
		t.Fatal(err)
	}
	if _, err := server.lookupOwned("u-owner", box.Name); status.Code(err) != codes.NotFound {
		t.Fatalf("canceled lookup code=%s err=%v", status.Code(err), err)
	}
}

func TestTimberTradeBoxServiceDescriptor(t *testing.T) {
	if tradeBoxServiceDesc.ServiceName != tradeBoxServiceName || len(tradeBoxServiceDesc.Methods) != 3 || len(tradeBoxServiceDesc.Streams) != 1 || !tradeBoxServiceDesc.Streams[0].ServerStreams {
		t.Fatalf("unexpected descriptor: %#v", tradeBoxServiceDesc)
	}
}

func outgoingVioletContext(t *testing.T, uid string) context.Context {
	t.Helper()
	md, ok := metadata.FromIncomingContext(violetAuthenticatedContext(uid))
	if !ok {
		t.Fatal("test authentication metadata unavailable")
	}
	return metadata.NewOutgoingContext(context.Background(), md)
}

func tradeBoxFromTrackResponse(t *testing.T, response []byte) timberTradeBox {
	t.Helper()
	number, typ, n := protowire.ConsumeTag(response)
	if n < 0 || number != 1 || typ != protowire.BytesType {
		t.Fatalf("response has no trade box: %x", response)
	}
	payload, consumed := protowire.ConsumeBytes(response[n:])
	if consumed < 0 {
		t.Fatal(protowire.ParseError(consumed))
	}
	box, err := parseTimberTradeBox(payload)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestTimberTradeBoxGRPCTwoUserExchange(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.ForceServerCodec(newHybridCodec()))
	registerTradeBoxServer(server, newTradeBoxServer())
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	dialContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, err := grpc.DialContext(dialContext, "bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(newHybridCodec())),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	create := func(uid, payload string) timberTradeBox {
		t.Helper()
		request := &rawMsg{b: createTradeBoxWire("tenants/current/users/current", "0", timberTradeBox{
			Payload: []byte(payload), Signature: []byte("sig-" + payload),
		})}
		var response rawMsg
		if err := connection.Invoke(outgoingVioletContext(t, uid), "/"+tradeBoxServiceName+"/CreateTradeBox", request, &response); err != nil {
			t.Fatal(err)
		}
		box, err := parseTimberTradeBox(response.b)
		if err != nil {
			t.Fatal(err)
		}
		return box
	}
	track := func(uid string) grpc.ClientStream {
		t.Helper()
		stream, err := connection.NewStream(outgoingVioletContext(t, uid), &grpc.StreamDesc{ServerStreams: true}, "/"+tradeBoxServiceName+"/TrackTradeBox")
		if err != nil {
			t.Fatal(err)
		}
		name := "tenants/current/users/current/tradeBoxes/0"
		if err := stream.SendMsg(&rawMsg{b: appendStringField(nil, 1, name)}); err != nil {
			t.Fatal(err)
		}
		if err := stream.CloseSend(); err != nil {
			t.Fatal(err)
		}
		return stream
	}

	firstStream := track("u-first")
	secondStream := track("u-second")
	var firstMissing, secondMissing rawMsg
	if err := firstStream.RecvMsg(&firstMissing); err != nil {
		t.Fatal(err)
	}
	if err := secondStream.RecvMsg(&secondMissing); err != nil {
		t.Fatal(err)
	}
	requireMissingTrackResponse(t, firstMissing.b)
	requireMissingTrackResponse(t, secondMissing.b)

	if got := create("u-first", "pokemon-a"); got.State != tradeBoxTrading {
		t.Fatalf("first create state=%d", got.State)
	}
	var initial rawMsg
	if err := firstStream.RecvMsg(&initial); err != nil {
		t.Fatal(err)
	}
	if got := tradeBoxFromTrackResponse(t, initial.b); got.State != tradeBoxTrading {
		t.Fatalf("first track state=%d", got.State)
	}

	if got := create("u-second", "pokemon-b"); got.State != tradeBoxTrading {
		t.Fatalf("second create state=%d", got.State)
	}
	var secondResult rawMsg
	if err := secondStream.RecvMsg(&secondResult); err != nil {
		t.Fatal(err)
	}
	if got := tradeBoxFromTrackResponse(t, secondResult.b); got.State != tradeBoxTraded || !bytes.Equal(got.Payload, []byte("pokemon-a")) {
		t.Fatalf("second result state=%d payload=%q", got.State, got.Payload)
	}

	var firstResult rawMsg
	if err := firstStream.RecvMsg(&firstResult); err != nil {
		t.Fatal(err)
	}
	if got := tradeBoxFromTrackResponse(t, firstResult.b); got.State != tradeBoxTraded || !bytes.Equal(got.Payload, []byte("pokemon-b")) {
		t.Fatalf("first result state=%d payload=%q", got.State, got.Payload)
	}
}
