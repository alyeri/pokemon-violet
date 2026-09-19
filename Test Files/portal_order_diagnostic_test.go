package main

import (
	"context"
	"fmt"
	gspb "npln.nintendo.net/npln-practice/proto/gamesync/v1"
	mmpb "npln.nintendo.net/npln-practice/proto/matchmaking/v1"
	"testing"
)

// Characterization only: this reproduces current behavior, not a claim that
// arrival-based sequences or the seeded room flags match the official service.
func TestPortalOrderDiagnostic(t *testing.T) {
	for _, mode := range []string{"BoxTrade", "NbrSingle"} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reverse=%t", mode, reverse), func(t *testing.T) {
				r := newSessionRegistry()
				m := newMatchmaker(r)
				var tickets [2]*mmpb.MatchmakingTicket
				for i, uid := range []string{"u-first", "u-second"} {
					ticket, err := m.CreateMatchmakingTicket(violetAuthenticatedContext(uid), &mmpb.CreateMatchmakingTicketRequest{MatchmakingTicket: &mmpb.MatchmakingTicket{MatchmakingConfig: mode, UserDefinitions: []*mmpb.UserDefinition{{User: "users/current"}}}})
					if err != nil {
						t.Fatal(err)
					}
					tickets[i] = ticket
					if _, ok := m.completeMatchmakingTicket(violetAuthenticatedContext(uid), ticket.Name, []string{"users/current"}); !ok {
						t.Fatal("match failed")
					}
				}
				var results [2]*mmpb.MatchmakingTicket
				for i, uid := range []string{"u-first", "u-second"} {
					var ok bool
					results[i], ok = m.completeMatchmakingTicket(violetAuthenticatedContext(uid), tickets[i].Name, []string{"users/current"})
					if !ok {
						t.Fatal("completion failed")
					}
				}
				g := newGamesyncServer(r)
				order := []int{0, 1}
				if reverse {
					order = []int{1, 0}
				}
				for _, i := range order {
					own := results[i].MatchedUserSessions[0]
					if _, err := g.IssueToken(context.Background(), &gspb.IssueTokenRequest{UserSession: own.UserSession, MatchmakingIdToken: own.MatchmakingIdToken}); err != nil {
						t.Fatal(err)
					}
				}
				gs := results[0].GameSession
				firstID := lastResourceSegment(results[0].MatchedUserSessions[0].UserSession)
				first, _ := g.sessionByID(firstID)
				want := int64(1)
				if reverse {
					want = 2
				}
				if first.Sequence != want {
					t.Fatalf("unexpected sequence %d", first.Sequence)
				}
				if g.owners[gs.Name]["docs/__gs/m"] != firstID {
					t.Fatal("room owner changed")
				}
				cp := g.documents[gs.Name]["docs/__gs/m"].Fields.Fields["cp"].GetBooleanValue()
				t.Logf("match-first=u-first room-owner=u-first sequence=%d cp=%t", first.Sequence, cp)
				if gs.CurrentParticipantCount != 2 || gs.State != mmpb.GameSession_ACTIVE {
					t.Fatal("membership changed during authentication")
				}
			})
		}
	}
}
