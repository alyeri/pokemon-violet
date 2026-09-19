package main

import (
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	gspb "npln.nintendo.net/npln-practice/proto/gamesync/v1"
)

// Keep transport backpressure out of the shared publication lock. A single
// writer preserves snapshot/change/echo ordering; saturation fails the stream
// rather than silently dropping signaling documents or growing without bound.
type gamesyncDelivery struct {
	grpc.BidiStreamingServer[gspb.KeepUserSessionRequest, gspb.KeepUserSessionResponse]
	queue chan *gspb.KeepUserSessionResponse
	done  chan struct{}
	mu    sync.Mutex
	err   error
	bytes int
}

func newGamesyncDelivery(stream grpc.BidiStreamingServer[gspb.KeepUserSessionRequest, gspb.KeepUserSessionResponse]) *gamesyncDelivery {
	d := &gamesyncDelivery{BidiStreamingServer: stream, queue: make(chan *gspb.KeepUserSessionResponse, 256), done: make(chan struct{})}
	go d.run()
	return d
}

func (d *gamesyncDelivery) stop(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failLocked(err)
}

func (d *gamesyncDelivery) failLocked(err error) {
	if d.err == nil {
		d.err = err
		close(d.done)
	}
}

func (d *gamesyncDelivery) failure() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

func (d *gamesyncDelivery) Send(message *gspb.KeepUserSessionResponse) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return d.err
	}
	n := proto.Size(message)
	if d.bytes+n > 4*1024*1024 {
		d.failLocked(status.Error(codes.ResourceExhausted, "gamesync delivery byte limit exceeded"))
		return d.err
	}
	select {
	case d.queue <- proto.Clone(message).(*gspb.KeepUserSessionResponse):
		d.bytes += n
		return nil
	default:
		d.failLocked(status.Error(codes.ResourceExhausted, "gamesync delivery queue full"))
		return d.err
	}
}

func (d *gamesyncDelivery) run() {
	for {
		select {
		case <-d.done:
			return
		case <-d.Context().Done():
			d.stop(status.FromContextError(d.Context().Err()).Err())
			return
		case message := <-d.queue:
			if d.failure() != nil {
				return
			}
			if err := d.BidiStreamingServer.Send(message); err != nil {
				d.stop(err)
				return
			}
			d.mu.Lock()
			d.bytes -= proto.Size(message)
			d.mu.Unlock()
		}
	}
}

// Wake the handler on an asynchronous send failure even if the peer is not
// sending requests. Returning from the handler cancels the underlying gRPC
// stream, releasing the pending Recv/Send. At most one Recv is pending.
func (d *gamesyncDelivery) Recv() (*gspb.KeepUserSessionRequest, error) {
	type result struct {
		request *gspb.KeepUserSessionRequest
		err     error
	}
	resultCh := make(chan result, 1)
	go func() { request, err := d.BidiStreamingServer.Recv(); resultCh <- result{request, err} }()
	select {
	case <-d.done:
		return nil, d.failure()
	case <-d.Context().Done():
		return nil, status.FromContextError(d.Context().Err()).Err()
	case r := <-resultCh:
		return r.request, r.err
	}
}
