package main

import (
	"context"
	gspb "npln.nintendo.net/npln-practice/proto/gamesync/v1"
	mmpb "npln.nintendo.net/npln-practice/proto/matchmaking/v1"
	"testing"
)

func TestVioletGuestJoinReturnsOwnGamesyncIdentity(t *testing.T) {
	registry := newSessionRegistry()
	server := newGameSessionServer(registry)
	host := violetAuthenticatedContext("u-host")
	ticket, err := server.CreateGameSessionCreationTicket(host, &mmpb.CreateGameSessionCreationTicketRequest{GameSessionCreationTicket: &mmpb.GameSessionCreationTicket{MatchmakingConfig: violetUnionCircleConfig, UserDefinitions: []*mmpb.UserDefinition{{User: "users/current"}}, GameSession: &mmpb.GameSession{Password: "TeamCircle"}}})
	if err != nil {
		t.Fatal(err)
	}
	completed, ok := server.completeGameSessionCreationTicket(host, ticket.GetName(), []string{"users/current"})
	if !ok {
		t.Fatal("creation failed")
	}
	response, err := server.JoinGameSession(violetAuthenticatedContext("u-guest"), &mmpb.JoinGameSessionRequest{Name: completed.GetGameSession().GetName(), Password: "TeamCircle", UserDefinitions: []*mmpb.UserDefinition{{User: "users/current"}}, IncludeIdTokenUsers: []string{"tenants/current/users/current"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.GetGameSession().GetUserSessions()) != 2 {
		t.Fatal("room membership lost")
	}
	matched := response.GetMatchedUserSessions()
	if len(matched) != 1 || userIDFromPath(matched[0].GetUserDefinition().GetUser()) != "u-guest" {
		t.Fatal("join returned another player's identity")
	}
	gamesync := newGamesyncServer(registry)
	_, err = gamesync.IssueToken(context.Background(), &gspb.IssueTokenRequest{UserSession: "userSessions/" + lastResourceSegment(matched[0].GetUserSession()), MatchmakingIdToken: matched[0].GetMatchmakingIdToken()})
	if err != nil {
		t.Fatalf("guest Gamesync authentication: %v", err)
	}
}
