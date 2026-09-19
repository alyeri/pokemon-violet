package main

import (
	"context"
	"google.golang.org/protobuf/encoding/protowire"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type notificationTestStream struct {
	grpc.ServerStream
	ctx      context.Context
	name     string
	headers  chan struct{}
	messages chan []byte
}

func (s *notificationTestStream) SendMsg(m any) error {
	s.messages <- append([]byte(nil), m.(*rawMsg).b...)
	return nil
}

func notificationBytesField(t *testing.T, wire []byte, wanted protowire.Number) []byte {
	t.Helper()
	for len(wire) > 0 {
		n, typ, size := protowire.ConsumeTag(wire)
		if size < 0 {
			t.Fatal("invalid tag")
		}
		wire = wire[size:]
		if n == wanted && typ == protowire.BytesType {
			value, size := protowire.ConsumeBytes(wire)
			if size < 0 {
				t.Fatal("invalid bytes")
			}
			return value
		}
		size = protowire.ConsumeFieldValue(n, typ, wire)
		if size < 0 {
			t.Fatal("invalid field")
		}
		wire = wire[size:]
	}
	t.Fatalf("missing field %d", wanted)
	return nil
}

func TestCompetitionNotificationPairCarriesMatchingKey(t *testing.T) {
	server := newCompetitionServer()
	streams := make([]*notificationTestStream, 2)
	done := make(chan error, 2)
	for i, uid := range []string{"u-first", "u-second"} {
		ctx, cancel := context.WithCancel(violetAuthenticatedContext(uid))
		defer cancel()
		streams[i] = &notificationTestStream{ctx: ctx, name: rankedCompetitionName(3) + "/participants/current/notification", headers: make(chan struct{}), messages: make(chan []byte, 1)}
		go func(s *notificationTestStream) { done <- server.receiveCompetitionNotification(s) }(streams[i])
	}
	var key string
	for _, stream := range streams {
		select {
		case wire := <-stream.messages:
			notification := notificationBytesField(t, wire, 2)
			got := string(notificationBytesField(t, notification, 4))
			if got == "" {
				t.Fatal("empty matching key")
			}
			if key != "" && got != key {
				t.Fatalf("different matching keys: %q != %q", got, key)
			}
			key = got
			if string(notificationBytesField(t, notification, 1)) != stream.name {
				t.Fatal("wrong notification resource")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("pair did not receive notification")
		}
	}
	for range streams {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("matched stream completion: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("matched stream did not complete after notification")
		}
	}
}

func (s *notificationTestStream) Context() context.Context { return s.ctx }
func (s *notificationTestStream) RecvMsg(m any) error {
	m.(*rawMsg).b = appendStringField(nil, 1, s.name)
	return nil
}
func (s *notificationTestStream) SendHeader(metadata.MD) error { close(s.headers); return nil }

func TestCompetitionNotificationSubscription(t *testing.T) {
	ctx, cancel := context.WithCancel(violetAuthenticatedContext("u-owner"))
	defer cancel()
	s := &notificationTestStream{ctx: ctx, name: rankedCompetitionName(3) + "/participants/current/notification", headers: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- newCompetitionServer().receiveCompetitionNotification(s) }()
	select {
	case <-s.headers:
	case err := <-done:
		t.Fatalf("failed before headers: %v", err)
	case <-time.After(time.Second):
		t.Fatal("no subscription headers")
	}
	select {
	case err := <-done:
		t.Fatalf("stream closed early: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if status.Code(err) != codes.Canceled {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream leaked")
	}
}

func TestCompetitionNotificationRejectsOtherParticipant(t *testing.T) {
	s := &notificationTestStream{ctx: violetAuthenticatedContext("u-owner"), name: rankedCompetitionName(3) + "/participants/u-other/notification", headers: make(chan struct{})}
	if err := newCompetitionServer().receiveCompetitionNotification(s); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("other participant: %v", err)
	}
}
