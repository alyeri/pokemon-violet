package main

// competition implements the first Timber contract used by Pokemon
// Scarlet/Violet's Battle Stadium. Timber is title-specific, so this service
// uses the same carefully bounded protobuf-wire approach as trade_box.go.

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
)

const competitionServiceName = "nn.npln.timber.v1.CompetitionService"

type rankedNotification struct {
	name        string
	matchingKey string
	etag        string
	updateTime  time.Time
}

type rankedNotificationWaiter struct {
	uid  string
	wake chan struct{}
	ctx  context.Context
}

type competitionServer struct {
	mu            sync.Mutex
	waiters       map[string][]*rankedNotificationWaiter
	notifications map[string]rankedNotification
	ranked        *rankedStore
}

func newCompetitionServer(stores ...*rankedStore) *competitionServer {
	server := &competitionServer{
		waiters:       make(map[string][]*rankedNotificationWaiter),
		notifications: make(map[string]rankedNotification),
	}
	if len(stores) > 0 {
		server.ranked = stores[0]
	}
	return server
}

func validateCompetitionUserName(name string) error {
	parts := strings.Split(name, "/")
	if len(parts) != 4 || parts[0] != "tenants" || parts[2] != "competitionUsers" ||
		(parts[1] != "current" && parts[1] != nplnTenantID) || parts[3] != "current" {
		return status.Error(codes.InvalidArgument, "invalid Violet competition-user resource name")
	}
	return nil
}

func rankedCompetitionName(competitionType uint64) string {
	kind := "singles"
	if competitionType == 4 {
		kind = "doubles"
	}
	return nplnTenant + "/competitions/local-ranked-" + kind + "-1"
}

func rankedCompetitionTypeFromName(name string) (uint64, error) {
	parts := strings.Split(name, "/")
	if len(parts) != 4 || parts[0] != "tenants" ||
		(parts[1] != "current" && parts[1] != nplnTenantID) || parts[2] != "competitions" {
		return 0, status.Error(codes.InvalidArgument, "invalid Violet ranked competition resource name")
	}
	switch parts[3] {
	case "local-ranked-singles-1":
		return 3, nil
	case "local-ranked-doubles-1":
		return 4, nil
	default:
		return 0, status.Error(codes.InvalidArgument, "unknown Violet ranked competition")
	}
}

func appendEmptyMessageField(out []byte, number protowire.Number) []byte {
	out = protowire.AppendTag(out, number, protowire.BytesType)
	return protowire.AppendBytes(out, nil)
}

func marshalCompetitionUser(name string, battleStats ...int32) []byte {
	// nn.npln.timber.v1.CompetitionUser:
	//   1 name, 4 language_code, 13 battle_stats, 14 category,
	//   15 current_ranked_singles_competition,
	//   17 current_ranked_doubles_competition, 19 create_time.
	//
	// Ranked discovery returns two active local seasons, so the current-user
	// resource must advertise the same resources. Leaving those references
	// absent while SearchCompetitions returned active seasons produced an
	// inconsistent state in Violet's ranked-screen callback.
	out := appendStringField(nil, 1, name)
	out = appendStringField(out, 4, "en")
	stats := []byte(nil)
	if len(battleStats) > 0 && battleStats[0] != 0 {
		stats = appendVarintField(stats, 1, uint64(battleStats[0]))
	}
	if len(battleStats) > 1 && battleStats[1] != 0 {
		stats = appendVarintField(stats, 2, uint64(battleStats[1]))
	}
	if len(stats) == 0 {
		out = appendEmptyMessageField(out, 13)
	} else {
		out = appendBytesField(out, 13, stats)
	}
	out = protowire.AppendTag(out, 14, protowire.VarintType)
	out = protowire.AppendVarint(out, 3)
	out = appendStringField(out, 15, rankedCompetitionName(3))
	out = appendStringField(out, 17, rankedCompetitionName(4))
	return appendBytesField(out, 19, marshalTimestamp(time.Now().UTC().Add(-24*time.Hour)))
}

func (s *competitionServer) GetCompetitionUser(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	uid, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	name, err := parseProtoStringMessage(message.b)
	if err != nil {
		return nil, err
	}
	if err := validateCompetitionUserName(name); err != nil {
		return nil, err
	}
	battles, disconnects := s.ranked.battleStats(uid)
	log.Printf("[NPLN Timber] GetCompetitionUser name=%q battles=%d disconnects=%d", name, battles, disconnects)
	return &rawMsg{b: marshalCompetitionUser(name, battles, disconnects)}, nil
}

func validateCompetitionParticipantName(name string) error {
	parts := strings.Split(name, "/")
	if len(parts) != 6 || parts[0] != "tenants" ||
		(parts[1] != "current" && parts[1] != nplnTenantID) ||
		parts[2] != "competitions" || parts[4] != "participants" || parts[5] == "" ||
		(parts[3] != "local-ranked-singles-1" && parts[3] != "local-ranked-doubles-1") {
		return status.Error(codes.InvalidArgument, "invalid Violet competition-participant resource name")
	}
	return nil
}

func marshalCompetitionParticipant(name string, now time.Time, ranked ...rankedStats) []byte {
	// nn.npln.timber.v1.CompetitionParticipant. This is the local participant
	// referenced by CompetitionUser. Zero-valued counters remain absent under
	// proto3; Violet receives an active MASTER participant with a deterministic
	// initial rating and lifecycle timestamps.
	stats := defaultRankedStats()
	if len(ranked) > 0 {
		stats = ranked[0]
	}
	out := appendStringField(nil, 1, name)
	out = appendVarintField(out, 2, 1) // Local slot.
	out = appendStringField(out, 3, "nextendo-local")
	out = appendStringField(out, 4, "+00:00")
	out = appendVarintField(out, 6, 2) // ACTIVE.
	out = appendBytesField(out, 7, marshalTimestamp(now.Add(-24*time.Hour)))
	out = appendBytesField(out, 8, marshalTimestamp(now.Add(-24*time.Hour)))
	out = appendVarintField(out, 13, uint64(stats.Rating))
	out = appendVarintField(out, 14, uint64(stats.Rank))
	out = appendVarintField(out, 15, uint64(stats.MatchCount))
	out = appendVarintField(out, 16, uint64(stats.WinCount))
	out = appendVarintField(out, 17, uint64(stats.LoseCount))
	out = appendVarintField(out, 18, uint64(stats.DrawCount))
	out = appendVarintField(out, 19, uint64(stats.DisconnectCount))
	out = appendVarintField(out, 20, uint64(stats.NoContestCount))
	out = appendVarintField(out, 21, uint64(stats.WinStreakCount))
	out = appendVarintField(out, 22, uint64(stats.LoseStreakCount))
	out = appendVarintField(out, 23, uint64(stats.Point))
	out = appendVarintField(out, 24, uint64(stats.MaxPoint))
	out = appendVarintField(out, 25, 3) // MASTER.
	return out
}

func (s *competitionServer) GetCompetitionParticipant(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	uid, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	name, err := parseProtoStringMessage(message.b)
	if err != nil {
		return nil, err
	}
	if err := validateCompetitionParticipantName(name); err != nil {
		return nil, err
	}
	parts := strings.Split(name, "/")
	stats := s.ranked.stats(parts[3], uid)
	log.Printf("[NPLN Timber] GetCompetitionParticipant name=%q state=ACTIVE rating=%d rank=%d matches=%d wins=%d losses=%d", name, stats.Rating, stats.Rank, stats.MatchCount, stats.WinCount, stats.LoseCount)
	return &rawMsg{b: marshalCompetitionParticipant(name, time.Now().UTC(), stats)}, nil
}

func rankedNotificationCompetition(name string) (string, error) {
	parts := strings.Split(name, "/")
	if len(parts) != 7 || parts[4] != "participants" || parts[5] == "" || parts[6] != "notification" {
		return "", status.Error(codes.InvalidArgument, "invalid Violet competition notification resource name")
	}
	competition := strings.Join(parts[:4], "/")
	kind, err := rankedCompetitionTypeFromName(competition)
	if err != nil {
		return "", err
	}
	return rankedCompetitionName(kind), nil
}

func rankedNotificationKey(uid, competition string) string { return uid + "\x00" + competition }

func marshalCompetitionNotification(name, matchingKey, etag string, now time.Time) []byte {
	out := appendStringField(nil, 1, name)
	out = appendBytesField(out, 2, marshalTimestamp(now))
	out = appendStringField(out, 4, matchingKey)
	if etag != "" {
		out = appendStringField(out, 99, etag)
	}
	return out
}

func (s *competitionServer) GetCompetitionNotification(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	uid, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	name, err := parseProtoStringMessage(message.b)
	if err != nil {
		return nil, err
	}
	competition, err := rankedNotificationCompetition(name)
	if err != nil {
		return nil, err
	}
	if participant := strings.Split(name, "/")[5]; participant != "current" && participant != uid {
		return nil, status.Error(codes.PermissionDenied, "notification participant does not match caller")
	}
	s.mu.Lock()
	notification, ok := s.notifications[rankedNotificationKey(uid, competition)]
	s.mu.Unlock()
	if !ok {
		return nil, status.Error(codes.NotFound, "no competition notification is pending")
	}
	log.Printf("[NPLN Timber] GetCompetitionNotification uid=%q competition=%q matching_key=%q", uid, competition, notification.matchingKey)
	return &rawMsg{b: marshalCompetitionNotification(name, notification.matchingKey, notification.etag, notification.updateTime)}, nil
}

type searchCompetitionsRequest struct {
	tenant   string
	pageSize uint64
	view     uint64
	types    []uint64
}

func parseSearchCompetitionsRequest(data []byte) (searchCompetitionsRequest, error) {
	var request searchCompetitionsRequest
	for len(data) > 0 {
		number, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return request, protowire.ParseError(n)
		}
		data = data[n:]
		switch number {
		case 1:
			if typ != protowire.BytesType || request.tenant != "" {
				return request, status.Error(codes.InvalidArgument, "invalid ranked competition tenant")
			}
			value, consumed := protowire.ConsumeString(data)
			if consumed < 0 {
				return request, protowire.ParseError(consumed)
			}
			request.tenant, n = value, consumed
		case 2, 4:
			if typ != protowire.VarintType {
				return request, status.Error(codes.InvalidArgument, "invalid ranked competition scalar")
			}
			value, consumed := protowire.ConsumeVarint(data)
			if consumed < 0 {
				return request, protowire.ParseError(consumed)
			}
			if number == 2 {
				request.pageSize = value
			} else {
				request.view = value
			}
			n = consumed
		case 6:
			if typ != protowire.BytesType {
				return request, status.Error(codes.InvalidArgument, "ranked competition types must be packed")
			}
			packed, consumed := protowire.ConsumeBytes(data)
			if consumed < 0 {
				return request, protowire.ParseError(consumed)
			}
			for len(packed) > 0 {
				value, itemSize := protowire.ConsumeVarint(packed)
				if itemSize < 0 {
					return request, protowire.ParseError(itemSize)
				}
				request.types = append(request.types, value)
				packed = packed[itemSize:]
			}
			n = consumed
		default:
			consumed := protowire.ConsumeFieldValue(number, typ, data)
			if consumed < 0 {
				return request, protowire.ParseError(consumed)
			}
			n = consumed
		}
		data = data[n:]
	}
	if request.tenant != "tenants/current" && request.tenant != nplnTenant {
		return request, status.Error(codes.InvalidArgument, "invalid ranked competition tenant")
	}
	if request.pageSize < 1 || request.pageSize > 2 || request.view != 1 || len(request.types) == 0 || len(request.types) > 2 {
		return request, status.Error(codes.InvalidArgument, "unsupported ranked competition search")
	}
	for _, competitionType := range request.types {
		if competitionType != 3 && competitionType != 4 {
			return request, status.Error(codes.InvalidArgument, "only ranked singles and doubles may be searched")
		}
	}
	return request, nil
}

func appendVarintField(out []byte, number protowire.Number, value uint64) []byte {
	out = protowire.AppendTag(out, number, protowire.VarintType)
	return protowire.AppendVarint(out, value)
}

func appendStringMapField(out []byte, number protowire.Number, key, value string) []byte {
	entry := appendStringField(nil, 1, key)
	entry = appendStringField(entry, 2, value)
	return appendBytesField(out, number, entry)
}

func marshalRankedCompetition(competitionType uint64, now time.Time) []byte {
	return marshalRankedCompetitionNamed(rankedCompetitionName(competitionType), competitionType, now)
}

func marshalRankedCompetitionMinimal(name string, competitionType uint64) []byte {
	// BASIC diagnostic floor: identity only. This intentionally omits every
	// optional season field so a client abort can be separated from malformed
	// or title-specific application_data in the synthetic full resource.
	out := appendStringField(nil, 1, name)
	out = appendVarintField(out, 5, 1)
	return appendVarintField(out, 33, competitionType)
}

func marshalRankedCompetitionScheduled(name string, competitionType uint64, now time.Time) []byte {
	out := marshalRankedCompetitionMinimal(name, competitionType)
	out = appendBytesField(out, 26, marshalTimestamp(now.Add(-24*time.Hour)))
	out = appendBytesField(out, 27, marshalTimestamp(now.Add(365*24*time.Hour)))
	out = appendBytesField(out, 28, marshalTimestamp(now.Add(-24*time.Hour)))
	out = appendBytesField(out, 29, marshalTimestamp(now.Add(365*24*time.Hour)))
	return out
}

func marshalRankedCompetitionNamed(name string, competitionType uint64, now time.Time) []byte {
	kind := "singles"
	subtitle := "Single Battle"
	if competitionType == 4 {
		kind = "doubles"
		subtitle = "Double Battle"
	}
	competitionID := "local-ranked-" + kind + "-1"
	out := appendStringField(nil, 1, name)
	out = appendStringField(out, 2, nplnTenant+"/competitionAliases/"+competitionID)
	out = appendVarintField(out, 33, competitionType)
	out = appendVarintField(out, 5, 1)
	out = appendStringMapField(out, 6, "en", "Ranked Battles")
	out = appendStringMapField(out, 7, "en", subtitle)
	out = appendVarintField(out, 8, 3) // ANYBODY may read the public season.
	out = appendVarintField(out, 9, 1) // Only the operator/owner may mutate it.
	out = appendVarintField(out, 10, 1)
	out = appendBytesField(out, 34, []byte{3}) // MASTER category.
	out = appendVarintField(out, 14, 1)
	out = appendVarintField(out, 15, 100000)
	out = appendVarintField(out, 16, 1)
	out = appendStringField(out, 17, "RankBattle")
	out = appendVarintField(out, 18, 1) // The server creates matching keys.
	hours := make([]byte, 0, 24)
	for hour := uint64(0); hour < 24; hour++ {
		hours = protowire.AppendVarint(hours, hour)
	}
	out = appendBytesField(out, 19, hours)
	out = appendVarintField(out, 20, 9999)
	out = appendVarintField(out, 21, 9999)
	out = appendVarintField(out, 35, 1)
	// application_data is a message field. Presence is observable in the C++
	// client even when the contained map is empty; ranked discovery expects a
	// Competition-owned application-data object before it reads title-specific
	// rules. Keep the map empty until its Scarlet/Violet schema is observed.
	out = appendEmptyMessageField(out, 24)
	out = appendBytesField(out, 25, marshalTimestamp(now.Add(-48*time.Hour)))
	out = appendBytesField(out, 36, marshalTimestamp(now.Add(-24*time.Hour)))
	out = appendBytesField(out, 26, marshalTimestamp(now.Add(-24*time.Hour)))
	out = appendBytesField(out, 27, marshalTimestamp(now.Add(365*24*time.Hour)))
	out = appendBytesField(out, 28, marshalTimestamp(now.Add(-24*time.Hour)))
	out = appendBytesField(out, 29, marshalTimestamp(now.Add(365*24*time.Hour)))
	out = appendBytesField(out, 30, marshalTimestamp(now.Add(366*24*time.Hour)))
	out = appendBytesField(out, 31, marshalTimestamp(now.Add(367*24*time.Hour)))
	return out
}

func (s *competitionServer) GetCompetition(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	if _, err := authenticatedNPLNUID(ctx); err != nil {
		return nil, err
	}
	name, err := parseProtoStringMessage(message.b)
	if err != nil {
		return nil, err
	}
	competitionType, err := rankedCompetitionTypeFromName(name)
	if err != nil {
		return nil, err
	}
	resourceMode := strings.ToLower(strings.TrimSpace(os.Getenv("VIOLET_RANKED_RESOURCE_MODE")))
	if resourceMode == "regulated" {
		wire, err := marshalRankedCompetitionRegulated(name, competitionType, time.Now().UTC())
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		log.Printf("[NPLN Timber] GetCompetition type=%d mode=regulated", competitionType)
		return &rawMsg{b: wire}, nil
	}
	if resourceMode == "minimal" {
		log.Printf("[NPLN Timber] GetCompetition name=%q type=%d view=BASIC mode=minimal-diagnostic", name, competitionType)
		return &rawMsg{b: marshalRankedCompetitionMinimal(name, competitionType)}, nil
	}
	if resourceMode == "full-diagnostic" {
		log.Printf("[NPLN Timber] GetCompetition name=%q type=%d view=BASIC mode=full-diagnostic", name, competitionType)
		return &rawMsg{b: marshalRankedCompetitionNamed(name, competitionType, time.Now().UTC())}, nil
	}
	// The scheduled profile is the only resource shape verified by Violet. The
	// broader synthetic Competition (especially application_data and
	// matchmaking_config_name) makes nn.npln.Worker abort before the menu opens.
	log.Printf("[NPLN Timber] GetCompetition name=%q type=%d view=BASIC mode=scheduled", name, competitionType)
	return &rawMsg{b: marshalRankedCompetitionScheduled(name, competitionType, time.Now().UTC())}, nil
}

func (s *competitionServer) SearchCompetitions(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	if _, err := authenticatedNPLNUID(ctx); err != nil {
		return nil, err
	}
	request, err := parseSearchCompetitionsRequest(message.b)
	if err != nil {
		return nil, err
	}
	// A zero-result response is a valid discovery outcome and gives us a safe
	// diagnostic boundary: if Violet accepts it, the transport and response
	// envelope are sound and the abort is caused by one of the synthetic
	// Competition fields below. Keep this opt-in so the full implementation
	// remains covered by the normal wire-contract tests.
	discoveryMode := strings.ToLower(strings.TrimSpace(os.Getenv("VIOLET_RANKED_DISCOVERY_MODE")))
	if discoveryMode == "empty" {
		log.Printf("[NPLN Timber] SearchCompetitions view=%d page_size=%d types=%v results=0 mode=empty-diagnostic", request.view, request.pageSize, request.types)
		return &rawMsg{}, nil
	}
	now := time.Now().UTC()
	out := []byte(nil)
	if discoveryMode == "regulated" {
		for _, competitionType := range request.types {
			competition, err := marshalRankedCompetitionRegulated(rankedCompetitionName(competitionType), competitionType, now)
			if err != nil {
				return nil, status.Error(codes.FailedPrecondition, err.Error())
			}
			out = appendBytesField(out, 1, competition)
		}
		log.Printf("[NPLN Timber] SearchCompetitions types=%v mode=regulated", request.types)
		return &rawMsg{b: out}, nil
	}
	if discoveryMode != "full-diagnostic" {
		for _, competitionType := range request.types {
			competition := marshalRankedCompetitionScheduled(rankedCompetitionName(competitionType), competitionType, now)
			out = appendBytesField(out, 1, competition)
		}
		log.Printf("[NPLN Timber] SearchCompetitions view=%d page_size=%d types=%v results=%d mode=scheduled", request.view, request.pageSize, request.types, len(request.types))
		return &rawMsg{b: out}, nil
	}
	for _, competitionType := range request.types {
		out = appendBytesField(out, 1, marshalRankedCompetition(competitionType, now))
	}
	log.Printf("[NPLN Timber] SearchCompetitions view=%d page_size=%d types=%v results=%d mode=full-diagnostic", request.view, request.pageSize, request.types, len(request.types))
	return &rawMsg{b: out}, nil
}

type timberCompetitionService interface{}

// This is a server-streaming subscription, not a unary acknowledgement.
// Violet's receive loop (main+0x5e5b6c) ignores an unset response oneof.
// Case 2 contains CompetitionNotification, whose case 4 carries matching_key.
// It consumes this directly; an empty message does not trigger a Get RPC.
func (s *competitionServer) receiveCompetitionNotification(stream grpc.ServerStream) error {
	uid, err := authenticatedNPLNUID(stream.Context())
	if err != nil {
		return err
	}
	request := new(rawMsg)
	if err := stream.RecvMsg(request); err != nil {
		return err
	}
	name, err := parseProtoStringMessage(request.b)
	if err != nil {
		return status.Error(codes.InvalidArgument, "invalid notification request")
	}
	competition, err := rankedNotificationCompetition(name)
	if err != nil {
		return err
	}
	parts := strings.Split(name, "/")
	if parts[5] != "current" && parts[5] != uid {
		return status.Error(codes.PermissionDenied, "notification participant does not match caller")
	}
	if err := stream.SendHeader(metadata.MD{}); err != nil {
		return err
	}
	log.Printf("[NPLN Timber] ReceiveCompetitionNotification subscribed name=%q", name)
	wake := make(chan struct{})
	s.mu.Lock()
	queue := s.waiters[competition]
	active := queue[:0]
	for _, waiter := range queue {
		if waiter.ctx.Err() == nil {
			active = append(active, waiter)
		}
	}
	queue = active
	s.waiters[competition] = queue
	for _, waiter := range queue {
		if waiter.uid == uid {
			s.mu.Unlock()
			return status.Error(codes.AlreadyExists, "participant already waiting")
		}
	}
	paired := -1
	for i, waiter := range queue {
		if waiter.uid != uid {
			paired = i
			break
		}
	}
	if paired >= 0 {
		key, keyErr := newResourceUUID()
		if keyErr != nil {
			s.mu.Unlock()
			return keyErr
		}
		now := time.Now().UTC()
		other := queue[paired]
		s.notifications[rankedNotificationKey(other.uid, competition)] = rankedNotification{matchingKey: key, etag: key, updateTime: now}
		s.notifications[rankedNotificationKey(uid, competition)] = rankedNotification{matchingKey: key, etag: key, updateTime: now}
		queue = append(queue[:paired], queue[paired+1:]...)
		close(other.wake)
		close(wake)
		s.waiters[competition] = queue
		log.Printf("[NPLN Timber] Ranked pair formed competition=%q users=%q,%q matching_key=%q", competition, other.uid, uid, key)
	} else {
		s.waiters[competition] = append(queue, &rankedNotificationWaiter{uid: uid, wake: wake, ctx: stream.Context()})
	}
	s.mu.Unlock()
	select {
	case <-wake:
		s.mu.Lock()
		notification := s.notifications[rankedNotificationKey(uid, competition)]
		s.mu.Unlock()
		payload := marshalCompetitionNotification(name, notification.matchingKey, notification.etag, notification.updateTime)
		if err := stream.SendMsg(&rawMsg{b: appendBytesField(nil, 2, payload)}); err != nil {
			return err
		}
		log.Printf("[NPLN Timber] ReceiveCompetitionNotification matching notification sent uid=%q field=2 bytes=%d", uid, len(payload))
		// Violet awaits this Receive operation before it starts the title-side
		// search keyed by matching_key.  Keeping the server stream alive after
		// the one matching notification leaves that operation pending forever:
		// its message callback runs, but the Lua flow never advances to the
		// shared Casual/Ranked search engine.  This RPC is therefore one-shot
		// for a queued participant and must finish successfully after delivery.
		log.Printf("[NPLN Timber] ReceiveCompetitionNotification completed uid=%q after matching delivery", uid)
		return nil
	case <-stream.Context().Done():
		s.mu.Lock()
		queue := s.waiters[competition]
		for i, waiter := range queue {
			if waiter.uid == uid && waiter.wake == wake {
				s.waiters[competition] = append(queue[:i], queue[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	}
	log.Printf("[NPLN Timber] ReceiveCompetitionNotification closed name=%q", name)
	return status.FromContextError(stream.Context().Err()).Err()
}

func competitionUnaryHandler(fullMethod string, call func(*competitionServer, context.Context, *rawMsg) (*rawMsg, error)) grpc.MethodDesc {
	methodName := fullMethod[strings.LastIndexByte(fullMethod, '/')+1:]
	return grpc.MethodDesc{MethodName: methodName, Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		in := new(rawMsg)
		if err := dec(in); err != nil {
			return nil, err
		}
		if interceptor == nil {
			return call(srv.(*competitionServer), ctx, in)
		}
		info := &grpc.UnaryServerInfo{Server: srv, FullMethod: fullMethod}
		handler := func(ctx context.Context, req any) (any, error) {
			return call(srv.(*competitionServer), ctx, req.(*rawMsg))
		}
		return interceptor(ctx, in, info, handler)
	}}
}

var competitionServiceDesc = grpc.ServiceDesc{
	ServiceName: competitionServiceName,
	HandlerType: (*timberCompetitionService)(nil),
	Streams: []grpc.StreamDesc{{
		StreamName:    "ReceiveCompetitionNotification",
		ServerStreams: true,
		Handler: func(srv any, stream grpc.ServerStream) error {
			return srv.(*competitionServer).receiveCompetitionNotification(stream)
		},
	}},
	Methods: []grpc.MethodDesc{
		competitionUnaryHandler("/"+competitionServiceName+"/GetCompetition", (*competitionServer).GetCompetition),
		competitionUnaryHandler("/"+competitionServiceName+"/GetCompetitionUser", (*competitionServer).GetCompetitionUser),
		competitionUnaryHandler("/"+competitionServiceName+"/SearchCompetitions", (*competitionServer).SearchCompetitions),
		competitionUnaryHandler("/"+competitionServiceName+"/GetCompetitionParticipant", (*competitionServer).GetCompetitionParticipant),
		competitionUnaryHandler("/"+competitionServiceName+"/GetCompetitionNotification", (*competitionServer).GetCompetitionNotification),
	},
}

func registerCompetitionServer(server grpc.ServiceRegistrar, implementation *competitionServer) {
	server.RegisterService(&competitionServiceDesc, implementation)
}
