package main

import (
	"context"
	commonpb "npln.nintendo.net/npln-practice/proto/common"
	gspb "npln.nintendo.net/npln-practice/proto/gamesync/v1"
	mmpb "npln.nintendo.net/npln-practice/proto/matchmaking/v1"
	"testing"
)

func TestPortalPairing(t *testing.T) {
	for _, config := range []string{"BoxTrade", "NbrSingle", "RankBattle"} {
		t.Run(config, func(t *testing.T) {
			r := newSessionRegistry()
			m := newMatchmaker(r)
			create := func(uid, mode, code string) *mmpb.MatchmakingTicket {
				t.Helper()
				ticket, err := m.CreateMatchmakingTicket(violetAuthenticatedContext(uid), &mmpb.CreateMatchmakingTicketRequest{MatchmakingTicket: &mmpb.MatchmakingTicket{MatchmakingConfig: mode, UserDefinitions: []*mmpb.UserDefinition{{User: "users/current", Attributes: &commonpb.MapValue{Fields: map[string]*commonpb.Value{"password": gamesyncStringValue(code)}}}}}})
				if err != nil {
					t.Fatal(err)
				}
				return ticket
			}
			complete := func(uid string, ticket *mmpb.MatchmakingTicket) *mmpb.MatchmakingTicket {
				t.Helper()
				result, ok := m.completeMatchmakingTicket(violetAuthenticatedContext(uid), ticket.Name, []string{"users/current"})
				if !ok {
					t.Fatal("completion failed")
				}
				return result
			}
			first := create("u-first", config, "22446688")
			if complete("u-first", first).State != mmpb.MatchmakingTicket_SEARCHING {
				t.Fatal("solo ticket succeeded")
			}
			other := create("u-other", config, "12345678")
			if complete("u-other", other).State != mmpb.MatchmakingTicket_SEARCHING {
				t.Fatal("different codes paired")
			}
			second := create("u-second", config, "22446688")
			b := complete("u-second", second)
			a := complete("u-first", first)
			if a.State != mmpb.MatchmakingTicket_SUCCEEDED || b.State != a.State || a.GameSession.Name != b.GameSession.Name || a.GameSession.MaxParticipantCount != 2 || len(a.GameSession.UserSessions) != 2 {
				t.Fatal("pair mismatch")
			}
			g := newGamesyncServer(r)
			for _, result := range []*mmpb.MatchmakingTicket{a, b} {
				if len(result.MatchedUserSessions) != 1 {
					t.Fatal("foreign identity returned")
				}
				own := result.MatchedUserSessions[0]
				if _, err := g.IssueToken(context.Background(), &gspb.IssueTokenRequest{UserSession: own.UserSession, MatchmakingIdToken: own.MatchmakingIdToken}); err != nil {
					t.Fatal(err)
				}
			}
			if err := g.initializeFixedData(a.GameSession); err != nil {
				t.Fatal(err)
			}
			if got := g.documents[a.GameSession.Name]["docs/__gs/f"].Fields.Fields["mcn"].GetStringValue(); got != config {
				t.Fatalf("mcn=%s", got)
			}
			if _, err := m.CancelMatchmakingTicket(violetAuthenticatedContext("u-other"), &mmpb.CancelMatchmakingTicketRequest{Name: other.Name}); err != nil {
				t.Fatal(err)
			}
			replacement := create("u-new", config, "12345678")
			if complete("u-new", replacement).State != mmpb.MatchmakingTicket_SEARCHING {
				t.Fatal("paired with canceled ticket")
			}
		})
	}
	if publicMatchPoolKey(&mmpb.MatchmakingTicket{MatchmakingConfig: "BoxTrade"}) == publicMatchPoolKey(&mmpb.MatchmakingTicket{MatchmakingConfig: "NbrSingle"}) {
		t.Fatal("modes share pool")
	}
}
