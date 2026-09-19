package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gspb "npln.nintendo.net/npln-practice/proto/gamesync/v1"
)

type deliveryTestStream struct {
	grpc.BidiStreamingServer[gspb.KeepUserSessionRequest, gspb.KeepUserSessionResponse]
	ctx     context.Context
	gate    chan struct{}
	sent    chan string
	sendErr error
}

func (s *deliveryTestStream) Context() context.Context { return s.ctx }
func (s *deliveryTestStream) Send(m *gspb.KeepUserSessionResponse) error {
	select {
	case <-s.gate:
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
	if s.sendErr != nil {
		return s.sendErr
	}
	s.sent <- m.GetEcho()
	return nil
}
func (s *deliveryTestStream) Recv() (*gspb.KeepUserSessionRequest, error) {
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}
func deliveryEcho(s string) *gspb.KeepUserSessionResponse {
	return &gspb.KeepUserSessionResponse{ResponseType: &gspb.KeepUserSessionResponse_Echo{Echo: s}}
}
func testDelivery(t *testing.T) (*gamesyncDelivery, *deliveryTestStream) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &deliveryTestStream{ctx: ctx, gate: make(chan struct{}), sent: make(chan string, 300)}
	d := newGamesyncDelivery(s)
	t.Cleanup(func() { d.stop(status.Error(codes.Canceled, "test complete")); cancel() })
	return d, s
}

func TestGamesyncDeliverySlowPeerDoesNotBlockPublication(t *testing.T) {
	d, stream := testDelivery(t)
	subscriber := newGamesyncSubscriber(d)
	finished := make(chan error, 1)
	go func() {
		for i := 0; i < 100; i++ {
			if err := subscriber.send(deliveryEcho(fmt.Sprint(i))); err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow transport blocked publication")
	}
	close(stream.gate)
	for i := 0; i < 100; i++ {
		select {
		case got := <-stream.sent:
			if got != fmt.Sprint(i) {
				t.Fatalf("order: got %q, want %d", got, i)
			}
		case <-time.After(time.Second):
			t.Fatal("delivery stalled")
		}
	}
}

func TestGamesyncDeliveryOverflowFailsStream(t *testing.T) {
	d, _ := testDelivery(t)
	var err error
	for i := 0; i < 258; i++ {
		if err = d.Send(deliveryEcho("queued")); err != nil {
			break
		}
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("overflow: %v", err)
	}
	if _, err := d.Recv(); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("receive did not wake: %v", err)
	}
}

func TestGamesyncDeliverySendFailureWakesReceiver(t *testing.T) {
	d, stream := testDelivery(t)
	stream.sendErr = status.Error(codes.Unavailable, "test transport failure")
	close(stream.gate)
	if err := d.Send(deliveryEcho("test")); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := d.Recv(); result <- err }()
	select {
	case err := <-result:
		if status.Code(err) != codes.Unavailable {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("send error did not terminate receive")
	}
}
