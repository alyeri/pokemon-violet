package main

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	mmpb "npln.nintendo.net/npln-practice/proto/matchmaking/v1"
)

const violetAliasAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// violetRoomCode derives the six-character Union Circle code from the session
// identifier. Determinism keeps alias creation idempotent in the single Violet
// process while avoiding a second cross-title state store.
func violetRoomCode(gameSession string) string {
	var hash uint64 = 14695981039346656037
	for _, b := range []byte(lastResourceSegment(gameSession)) {
		hash ^= uint64(b)
		hash *= 1099511628211
	}
	code := make([]byte, 6)
	for i := range code {
		code[i] = violetAliasAlphabet[hash%uint64(len(violetAliasAlphabet))]
		hash /= uint64(len(violetAliasAlphabet))
	}
	return string(code)
}

func validVioletRoomCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, c := range code {
		if !strings.ContainsRune(violetAliasAlphabet, c) {
			return false
		}
	}
	return true
}

func (g *gameSessionServer) CreateGameSessionShortAlias(ctx context.Context, req *mmpb.CreateGameSessionShortAliasRequest) (*mmpb.GameSessionShortAlias, error) {
	uid, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	gameSession := req.GetGameSessionShortAlias().GetGameSession()
	if !validSessionName(gameSession) {
		return nil, status.Error(codes.InvalidArgument, "invalid game session for alias")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	gs := g.sessions[lastResourceSegment(gameSession)]
	if !sessionHasUID(gs, uid) {
		return nil, status.Error(codes.PermissionDenied, "active host membership required")
	}
	code := violetRoomCode(gs.GetName())
	alias := &mmpb.GameSessionShortAlias{
		// Violet's resource parser requires the capital G (main+0x4f725c).
		Name:        nplnTenant + "/GameSessionShortAliases/" + code,
		GameSession: gs.GetName(),
		ExpireTime:  timestamppb.New(time.Now().Add(time.Hour)),
	}
	g.aliases[code] = proto.Clone(alias).(*mmpb.GameSessionShortAlias)
	return alias, nil
}

func (g *gameSessionServer) GetGameSessionShortAlias(ctx context.Context, req *mmpb.GetGameSessionShortAliasRequest) (*mmpb.GameSessionShortAlias, error) {
	if _, err := authenticatedNPLNUID(ctx); err != nil {
		return nil, err
	}
	code := strings.ToUpper(lastResourceSegment(req.GetName()))
	if !validVioletRoomCode(code) {
		return nil, status.Error(codes.InvalidArgument, "invalid Union Circle code")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	alias := g.aliases[code]
	if alias == nil || alias.GetExpireTime().AsTime().Before(time.Now()) {
		delete(g.aliases, code)
		return nil, status.Error(codes.NotFound, "Union Circle code not found")
	}
	gs := g.sessions[lastResourceSegment(alias.GetGameSession())]
	if gs == nil || gs.GetState() != mmpb.GameSession_ACTIVE {
		delete(g.aliases, code)
		return nil, status.Error(codes.NotFound, "Union Circle group is no longer active")
	}
	return proto.Clone(alias).(*mmpb.GameSessionShortAlias), nil
}
