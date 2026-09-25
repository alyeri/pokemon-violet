package main

import (
	"testing"

	commonpb "npln.nintendo.net/npln-practice/proto/common"
	mmpb "npln.nintendo.net/npln-practice/proto/matchmaking/v1"
)

func TestRaidPublicSearchWithoutPostings(t *testing.T) {
	g := newGameSessionServer(newSessionRegistry())
	result, err := g.QueryGameSessions(violetAuthenticatedContext("raid-searcher"), &mmpb.QueryGameSessionsRequest{
		Tenant:                  "tenants/current",
		GameSessionSearchConfig: "tenants/current/gameSessionSearchConfigs/RaidPublicSearch",
		MinVacancyCount:         1,
		PageSize:                20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.GameSessions) != 0 {
		t.Fatalf("unexpected raid postings: %d", len(result.GameSessions))
	}
}

func TestRaidPublicHostCreation(t *testing.T) {
	g := newGameSessionServer(newSessionRegistry())
	fields := map[string]*commonpb.Value{
		"difficulty":     gamesyncIntegerValue(4),
		"is_distributed": gamesyncIntegerValue(0),
		"mons_no":        gamesyncIntegerValue(941),
		"raid_table_id":  gamesyncIntegerValue(4061),
	}
	ctx := violetAuthenticatedContext("raid-host")
	created, err := g.CreateGameSessionCreationTicket(ctx, &mmpb.CreateGameSessionCreationTicketRequest{
		Parent: "tenants/current",
		GameSessionCreationTicket: &mmpb.GameSessionCreationTicket{
			MatchmakingConfig: "tenants/current/matchmakingConfigs/RaidPublic",
			UserDefinitions: []*mmpb.UserDefinition{{
				User: "tenants/current/users/current", Team: "owner",
				Attributes: &commonpb.MapValue{Fields: fields},
			}},
			GameSession: &mmpb.GameSession{Properties: &commonpb.MapValue{Fields: fields}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := g.completeGameSessionCreationTicket(ctx, created.Name, []string{"tenants/current/users/current"})
	if !ok || result.GetState() != mmpb.GameSessionCreationTicket_SUCCEEDED {
		t.Fatalf("raid host creation did not succeed: ok=%t state=%s", ok, result.GetState())
	}
	if result.GameSession.MaxParticipantCount != 4 || !result.GameSession.IsPublic ||
		result.GameSession.Properties.Fields["raid_table_id"].GetIntegerValue() != 4061 {
		t.Fatalf("raid host session lost its room contract: %v", result.GameSession)
	}
	query, err := g.QueryGameSessions(violetAuthenticatedContext("raid-guest"), &mmpb.QueryGameSessionsRequest{
		Tenant:                  "tenants/current",
		GameSessionSearchConfig: "tenants/current/gameSessionSearchConfigs/RaidPublicSearch",
		MinVacancyCount:         1,
		Properties: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"difficulty": gamesyncIntegerValue(4), "is_distributed": gamesyncIntegerValue(0),
		}},
	})
	if err != nil || len(query.GameSessions) != 1 || query.GameSessions[0].Name != result.GameSession.Name {
		t.Fatalf("raid host not searchable: sessions=%d err=%v", len(query.GameSessions), err)
	}
	g.mu.Lock()
	g.registry.configs[result.GameSession.Name] = violetUnionCircleConfig
	g.mu.Unlock()
	query, err = g.QueryGameSessions(violetAuthenticatedContext("raid-guest"), &mmpb.QueryGameSessionsRequest{
		Tenant:                  "tenants/current",
		GameSessionSearchConfig: "tenants/current/gameSessionSearchConfigs/RaidPublicSearch",
		MinVacancyCount:         1,
	})
	if err != nil || len(query.GameSessions) != 0 {
		t.Fatalf("non-raid room leaked into raid search: sessions=%d err=%v", len(query.GameSessions), err)
	}
}
