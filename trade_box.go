package main

// trade_box implements the Pokemon Scarlet/Violet Timber surprise-trade
// service. The public NPLN schema is intentionally handled as protobuf wire
// data here because Timber is title-specific and is not part of the generated
// common NPLN bindings already vendored by this server.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	tradeBoxServiceName  = "nn.npln.timber.v1.TradeBoxService"
	tradeBoxTrading      = 1
	tradeBoxTraded       = 2
	maxTradeBoxRequest   = 128 << 10
	maxTradeBoxPayload   = 64 << 10
	maxTradeBoxSignature = 1024
)

type timberTradeBox struct {
	Name      string
	State     uint64
	Timestamp time.Time
	Payload   []byte
	Signature []byte
}

func cloneTimberTradeBox(in timberTradeBox) timberTradeBox {
	out := in
	out.Payload = append([]byte(nil), in.Payload...)
	out.Signature = append([]byte(nil), in.Signature...)
	return out
}

type createTradeBoxRequest struct {
	Parent     string
	TradeBox   timberTradeBox
	TradeBoxID string
}

func consumeStringField(data []byte) (string, int, error) {
	value, n := protowire.ConsumeString(data)
	if n < 0 {
		return "", 0, protowire.ParseError(n)
	}
	return value, n, nil
}

func consumeBytesField(data []byte) ([]byte, int, error) {
	value, n := protowire.ConsumeBytes(data)
	if n < 0 {
		return nil, 0, protowire.ParseError(n)
	}
	return append([]byte(nil), value...), n, nil
}

func skipProtoField(number protowire.Number, typ protowire.Type, data []byte) (int, error) {
	n := protowire.ConsumeFieldValue(number, typ, data)
	if n < 0 {
		return 0, protowire.ParseError(n)
	}
	return n, nil
}

func parseProtoStringMessage(data []byte) (string, error) {
	if len(data) > maxTradeBoxRequest {
		return "", status.Error(codes.ResourceExhausted, "request too large")
	}
	var value string
	for len(data) > 0 {
		number, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return "", protowire.ParseError(n)
		}
		data = data[n:]
		if number == 1 {
			if typ != protowire.BytesType || value != "" {
				return "", status.Error(codes.InvalidArgument, "invalid resource name")
			}
			var err error
			value, n, err = consumeStringField(data)
			if err != nil {
				return "", err
			}
		} else {
			var err error
			n, err = skipProtoField(number, typ, data)
			if err != nil {
				return "", err
			}
		}
		data = data[n:]
	}
	if value == "" || len(value) > 512 {
		return "", status.Error(codes.InvalidArgument, "resource name required")
	}
	return value, nil
}

func parseTimestamp(data []byte) (time.Time, error) {
	var seconds int64
	var nanos int32
	for len(data) > 0 {
		number, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return time.Time{}, protowire.ParseError(n)
		}
		data = data[n:]
		if (number == 1 || number == 2) && typ == protowire.VarintType {
			value, consumed := protowire.ConsumeVarint(data)
			if consumed < 0 {
				return time.Time{}, protowire.ParseError(consumed)
			}
			if number == 1 {
				seconds = int64(value)
			} else {
				nanos = int32(value)
			}
			n = consumed
		} else {
			var err error
			n, err = skipProtoField(number, typ, data)
			if err != nil {
				return time.Time{}, err
			}
		}
		data = data[n:]
	}
	if nanos < 0 || nanos >= 1_000_000_000 {
		return time.Time{}, status.Error(codes.InvalidArgument, "invalid timestamp")
	}
	return time.Unix(seconds, int64(nanos)).UTC(), nil
}

func parseTimberTradeBox(data []byte) (timberTradeBox, error) {
	var box timberTradeBox
	for len(data) > 0 {
		number, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return box, protowire.ParseError(n)
		}
		data = data[n:]
		switch number {
		case 1:
			if typ != protowire.BytesType {
				return box, status.Error(codes.InvalidArgument, "invalid trade-box name")
			}
			var err error
			box.Name, n, err = consumeStringField(data)
			if err != nil {
				return box, err
			}
		case 2:
			if typ != protowire.VarintType {
				return box, status.Error(codes.InvalidArgument, "invalid trade-box state")
			}
			value, consumed := protowire.ConsumeVarint(data)
			if consumed < 0 {
				return box, protowire.ParseError(consumed)
			}
			box.State, n = value, consumed
		case 3:
			if typ != protowire.BytesType {
				return box, status.Error(codes.InvalidArgument, "invalid trade-box timestamp")
			}
			value, consumed, err := consumeBytesField(data)
			if err != nil {
				return box, err
			}
			box.Timestamp, err = parseTimestamp(value)
			if err != nil {
				return box, err
			}
			n = consumed
		case 4, 5:
			if typ != protowire.BytesType {
				return box, status.Error(codes.InvalidArgument, "invalid trade-box binary field")
			}
			value, consumed, err := consumeBytesField(data)
			if err != nil {
				return box, err
			}
			if number == 4 {
				box.Payload = value
			} else {
				box.Signature = value
			}
			n = consumed
		default:
			var err error
			n, err = skipProtoField(number, typ, data)
			if err != nil {
				return box, err
			}
		}
		data = data[n:]
	}
	if len(box.Name) > 512 || len(box.Payload) > maxTradeBoxPayload || len(box.Signature) > maxTradeBoxSignature {
		return box, status.Error(codes.ResourceExhausted, "trade-box field too large")
	}
	return box, nil
}

func parseCreateTradeBoxRequest(data []byte) (createTradeBoxRequest, error) {
	var req createTradeBoxRequest
	if len(data) > maxTradeBoxRequest {
		return req, status.Error(codes.ResourceExhausted, "request too large")
	}
	for len(data) > 0 {
		number, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return req, protowire.ParseError(n)
		}
		data = data[n:]
		if (number == 1 || number == 2 || number == 3) && typ == protowire.BytesType {
			value, consumed, err := consumeBytesField(data)
			if err != nil {
				return req, err
			}
			switch number {
			case 1:
				req.Parent = string(value)
			case 2:
				req.TradeBox, err = parseTimberTradeBox(value)
				if err != nil {
					return req, err
				}
			case 3:
				req.TradeBoxID = string(value)
			}
			n = consumed
		} else {
			var err error
			n, err = skipProtoField(number, typ, data)
			if err != nil {
				return req, err
			}
		}
		data = data[n:]
	}
	if req.Parent == "" || len(req.Parent) > 512 || len(req.TradeBoxID) > 128 {
		return req, status.Error(codes.InvalidArgument, "parent and valid trade_box_id required")
	}
	if len(req.TradeBox.Payload) == 0 || len(req.TradeBox.Signature) == 0 {
		return req, status.Error(codes.InvalidArgument, "trade payload and signature required")
	}
	if req.TradeBox.State != 0 && req.TradeBox.State != tradeBoxTrading {
		return req, status.Error(codes.InvalidArgument, "new trade box must be unspecified or trading")
	}
	return req, nil
}

func appendStringField(out []byte, number protowire.Number, value string) []byte {
	if value == "" {
		return out
	}
	out = protowire.AppendTag(out, number, protowire.BytesType)
	return protowire.AppendString(out, value)
}

func appendBytesField(out []byte, number protowire.Number, value []byte) []byte {
	if len(value) == 0 {
		return out
	}
	out = protowire.AppendTag(out, number, protowire.BytesType)
	return protowire.AppendBytes(out, value)
}

func marshalTimestamp(value time.Time) []byte {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	out := protowire.AppendTag(nil, 1, protowire.VarintType)
	out = protowire.AppendVarint(out, uint64(value.Unix()))
	if value.Nanosecond() != 0 {
		out = protowire.AppendTag(out, 2, protowire.VarintType)
		out = protowire.AppendVarint(out, uint64(value.Nanosecond()))
	}
	return out
}

func marshalTimberTradeBox(box timberTradeBox) []byte {
	out := appendStringField(nil, 1, box.Name)
	if box.State != 0 {
		out = protowire.AppendTag(out, 2, protowire.VarintType)
		out = protowire.AppendVarint(out, box.State)
	}
	out = appendBytesField(out, 3, marshalTimestamp(box.Timestamp))
	out = appendBytesField(out, 4, box.Payload)
	return appendBytesField(out, 5, box.Signature)
}

func marshalTrackTradeBoxResponse(box *timberTradeBox, missing bool, keepAlive time.Duration) []byte {
	var out []byte
	if box != nil {
		out = appendBytesField(out, 1, marshalTimberTradeBox(*box))
	}
	if missing {
		out = protowire.AppendTag(out, 2, protowire.VarintType)
		out = protowire.AppendVarint(out, 1)
	}
	if keepAlive > 0 {
		duration := protowire.AppendTag(nil, 1, protowire.VarintType)
		duration = protowire.AppendVarint(duration, uint64(keepAlive/time.Second))
		keepAliveMessage := appendBytesField(nil, 1, duration)
		out = appendBytesField(out, 3, keepAliveMessage)
	}
	return out
}

type tradeBoxEntry struct {
	owner    string
	box      timberTradeBox
	result   timberTradeBox
	created  bool
	matched  bool
	canceled bool
	notify   chan struct{}
}

type tradeBoxServer struct {
	mu      sync.Mutex
	boxes   map[string]*tradeBoxEntry
	waiting []string
}

func tradeBoxStorageKey(owner, id string) string {
	return owner + "\x00" + id
}

func newTradeBoxServer() *tradeBoxServer {
	return &tradeBoxServer{boxes: make(map[string]*tradeBoxEntry)}
}

func validTradeBoxParent(parent, owner string) bool {
	parts := strings.Split(strings.TrimSuffix(parent, "/tradeBoxes"), "/")
	if len(parts) != 4 || parts[0] != "tenants" || parts[2] != "users" {
		return false
	}
	return (parts[1] == "current" || parts[1] == nplnTenantID) && (parts[3] == "current" || parts[3] == owner)
}

func validTradeBoxID(value string) bool {
	if value == "" {
		return true
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func (s *tradeBoxServer) signalLocked(entry *tradeBoxEntry) {
	select {
	case entry.notify <- struct{}{}:
	default:
	}
}

func (s *tradeBoxServer) createForOwner(owner string, req createTradeBoxRequest) (timberTradeBox, error) {
	if !validTradeBoxParent(req.Parent, owner) {
		return timberTradeBox{}, status.Error(codes.InvalidArgument, "invalid Violet trade-box parent")
	}
	if !validTradeBoxID(req.TradeBoxID) {
		return timberTradeBox{}, status.Error(codes.InvalidArgument, "invalid trade_box_id")
	}
	id := req.TradeBoxID
	if id == "" {
		var err error
		id, err = newResourceUUID()
		if err != nil {
			return timberTradeBox{}, status.Error(codes.Internal, "cannot allocate trade box")
		}
	}
	parent := strings.TrimSuffix(req.Parent, "/tradeBoxes")
	name := parent + "/tradeBoxes/" + id
	storageKey := tradeBoxStorageKey(owner, id)
	now := time.Now().UTC()
	box := cloneTimberTradeBox(req.TradeBox)
	box.Name, box.State, box.Timestamp = name, tradeBoxTrading, now
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.boxes[storageKey]
	if entry != nil && (entry.created || entry.canceled) {
		return timberTradeBox{}, status.Error(codes.AlreadyExists, "trade box already exists")
	}
	if entry == nil {
		entry = &tradeBoxEntry{owner: owner, notify: make(chan struct{}, 1)}
		s.boxes[storageKey] = entry
	}
	entry.box = box
	entry.created = true
	s.signalLocked(entry)

	for len(s.waiting) > 0 {
		candidateID := s.waiting[0]
		s.waiting = s.waiting[1:]
		candidate := s.boxes[candidateID]
		if candidate == nil || !candidate.created || candidate.canceled || candidate.matched {
			continue
		}
		if candidate.owner == owner {
			s.waiting = append(s.waiting, candidateID)
			break
		}
		candidate.result = cloneTimberTradeBox(candidate.box)
		candidate.result.State = tradeBoxTraded
		candidate.result.Timestamp = now
		candidate.result.Payload = append([]byte(nil), box.Payload...)
		candidate.result.Signature = append([]byte(nil), box.Signature...)
		entry.result = cloneTimberTradeBox(box)
		entry.result.State = tradeBoxTraded
		entry.result.Timestamp = now
		entry.result.Payload = append([]byte(nil), candidate.box.Payload...)
		entry.result.Signature = append([]byte(nil), candidate.box.Signature...)
		candidate.matched, entry.matched = true, true
		s.signalLocked(candidate)
		s.signalLocked(entry)
		log.Printf("[NPLN Timber] paired trade boxes first=%q second=%q payload_bytes=%d/%d signature_bytes=%d/%d",
			candidate.box.Name, entry.box.Name, len(candidate.box.Payload), len(entry.box.Payload), len(candidate.box.Signature), len(entry.box.Signature))
		return cloneTimberTradeBox(box), nil
	}
	s.waiting = append(s.waiting, storageKey)
	return cloneTimberTradeBox(box), nil
}

func (s *tradeBoxServer) lookupOwned(owner, name string) (*tradeBoxEntry, error) {
	id := lastResourceSegment(name)
	storageKey := tradeBoxStorageKey(owner, id)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.boxes[storageKey]
	if entry == nil || !entry.created || entry.canceled {
		return nil, status.Error(codes.NotFound, "trade box not found")
	}
	if entry.owner != owner {
		return nil, status.Error(codes.PermissionDenied, "trade box belongs to another caller")
	}
	return entry, nil
}

// trackForOwner returns a per-user entry even when TrackTradeBox arrives before
// CreateTradeBox. Scarlet/Violet opens the server stream first and only creates
// the resource after that stream remains healthy.
func (s *tradeBoxServer) trackForOwner(owner, name string) (*tradeBoxEntry, error) {
	if err := validateTradeBoxResourceName(name); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	parts := strings.Split(name, "/")
	if (parts[3] != "current" && parts[3] != owner) || !validTradeBoxID(parts[5]) {
		return nil, status.Error(codes.InvalidArgument, "invalid Violet trade-box resource")
	}
	storageKey := tradeBoxStorageKey(owner, parts[5])
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.boxes[storageKey]
	if entry == nil || entry.canceled {
		entry = &tradeBoxEntry{owner: owner, box: timberTradeBox{Name: name}, notify: make(chan struct{}, 1)}
		s.boxes[storageKey] = entry
	}
	return entry, nil
}

func (s *tradeBoxServer) cancelForOwner(owner, name string, remove bool) error {
	id := lastResourceSegment(name)
	storageKey := tradeBoxStorageKey(owner, id)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.boxes[storageKey]
	if entry == nil {
		if remove {
			return nil
		}
		return status.Error(codes.NotFound, "trade box not found")
	}
	if entry.owner != owner {
		return status.Error(codes.PermissionDenied, "trade box belongs to another caller")
	}
	entry.canceled = true
	s.signalLocked(entry)
	if remove {
		delete(s.boxes, storageKey)
	}
	return nil
}

func (s *tradeBoxServer) CreateTradeBox(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	owner, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	req, err := parseCreateTradeBoxRequest(message.b)
	if err != nil {
		return nil, err
	}
	box, err := s.createForOwner(owner, req)
	if err != nil {
		return nil, err
	}
	log.Printf("[NPLN Timber] CreateTradeBox name=%q owner=%q payload_bytes=%d signature_bytes=%d", box.Name, owner, len(box.Payload), len(box.Signature))
	return &rawMsg{b: marshalTimberTradeBox(box)}, nil
}

func (s *tradeBoxServer) DeleteTradeBox(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	owner, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	name, err := parseProtoStringMessage(message.b)
	if err != nil {
		return nil, err
	}
	if err := s.cancelForOwner(owner, name, true); err != nil {
		return nil, err
	}
	log.Printf("[NPLN Timber] DeleteTradeBox name=%q owner=%q", name, owner)
	return &rawMsg{}, nil
}

func (s *tradeBoxServer) CancelTradeBox(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	owner, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	name, err := parseProtoStringMessage(message.b)
	if err != nil {
		return nil, err
	}
	if err := s.cancelForOwner(owner, name, false); err != nil {
		return nil, err
	}
	log.Printf("[NPLN Timber] CancelTradeBox name=%q owner=%q", name, owner)
	return &rawMsg{}, nil
}

func (s *tradeBoxServer) TrackTradeBox(message *rawMsg, stream grpc.ServerStream) error {
	owner, err := authenticatedNPLNUID(stream.Context())
	if err != nil {
		return err
	}
	name, err := parseProtoStringMessage(message.b)
	if err != nil {
		return err
	}
	entry, err := s.trackForOwner(owner, name)
	if err != nil {
		return err
	}
	log.Printf("[NPLN Timber] TrackTradeBox name=%q owner=%q", name, owner)

	s.mu.Lock()
	if entry.created && entry.matched {
		result := cloneTimberTradeBox(entry.result)
		s.mu.Unlock()
		return stream.SendMsg(&rawMsg{b: marshalTrackTradeBoxResponse(&result, false, 0)})
	}
	created := entry.created
	initial := cloneTimberTradeBox(entry.box)
	s.mu.Unlock()
	if created {
		if err := stream.SendMsg(&rawMsg{b: marshalTrackTradeBoxResponse(&initial, false, 0)}); err != nil {
			return err
		}
	} else if err := stream.SendMsg(&rawMsg{b: marshalTrackTradeBoxResponse(nil, true, 0)}); err != nil {
		return err
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-entry.notify:
			s.mu.Lock()
			created, matched, canceled := entry.created, entry.matched, entry.canceled
			box := cloneTimberTradeBox(entry.box)
			result := cloneTimberTradeBox(entry.result)
			s.mu.Unlock()
			if canceled && !matched {
				return status.Error(codes.Canceled, "trade box canceled")
			}
			if matched {
				return stream.SendMsg(&rawMsg{b: marshalTrackTradeBoxResponse(&result, false, 0)})
			}
			if created {
				if err := stream.SendMsg(&rawMsg{b: marshalTrackTradeBoxResponse(&box, false, 0)}); err != nil {
					return err
				}
			}
		case <-ticker.C:
			if err := stream.SendMsg(&rawMsg{b: marshalTrackTradeBoxResponse(nil, false, 60*time.Second)}); err != nil {
				return err
			}
		}
	}
}

type timberTradeBoxService interface{}

func tradeBoxUnaryHandler(fullMethod string, call func(*tradeBoxServer, context.Context, *rawMsg) (*rawMsg, error)) grpc.MethodDesc {
	methodName := fullMethod[strings.LastIndexByte(fullMethod, '/')+1:]
	return grpc.MethodDesc{MethodName: methodName, Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		in := new(rawMsg)
		if err := dec(in); err != nil {
			return nil, err
		}
		if interceptor == nil {
			return call(srv.(*tradeBoxServer), ctx, in)
		}
		info := &grpc.UnaryServerInfo{Server: srv, FullMethod: fullMethod}
		handler := func(ctx context.Context, req any) (any, error) {
			return call(srv.(*tradeBoxServer), ctx, req.(*rawMsg))
		}
		return interceptor(ctx, in, info, handler)
	}}
}

var tradeBoxServiceDesc = grpc.ServiceDesc{
	ServiceName: tradeBoxServiceName,
	HandlerType: (*timberTradeBoxService)(nil),
	Methods: []grpc.MethodDesc{
		tradeBoxUnaryHandler("/"+tradeBoxServiceName+"/CreateTradeBox", (*tradeBoxServer).CreateTradeBox),
		tradeBoxUnaryHandler("/"+tradeBoxServiceName+"/DeleteTradeBox", (*tradeBoxServer).DeleteTradeBox),
		tradeBoxUnaryHandler("/"+tradeBoxServiceName+"/CancelTradeBox", (*tradeBoxServer).CancelTradeBox),
	},
	Streams: []grpc.StreamDesc{{
		StreamName:    "TrackTradeBox",
		ServerStreams: true,
		Handler: func(srv any, stream grpc.ServerStream) error {
			in := new(rawMsg)
			if err := stream.RecvMsg(in); err != nil {
				return err
			}
			return srv.(*tradeBoxServer).TrackTradeBox(in, stream)
		},
	}},
}

func registerTradeBoxServer(server grpc.ServiceRegistrar, implementation *tradeBoxServer) {
	server.RegisterService(&tradeBoxServiceDesc, implementation)
}

func validateTradeBoxResourceName(name string) error {
	parts := strings.Split(name, "/")
	if len(parts) != 6 || parts[0] != "tenants" || parts[2] != "users" || parts[4] != "tradeBoxes" || parts[5] == "" ||
		(parts[1] != "current" && parts[1] != nplnTenantID) {
		return fmt.Errorf("invalid trade-box resource %q", name)
	}
	return nil
}
