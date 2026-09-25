package main

import (
	"testing"

	commonpb "npln.nintendo.net/npln-practice/proto/common"
	mmpb "npln.nintendo.net/npln-practice/proto/matchmaking/v1"
)

func casualBattleTicket(mode int64) *mmpb.MatchmakingTicket {
	return &mmpb.MatchmakingTicket{
		MatchmakingConfig: "tenants/current/matchmakingConfigs/CasualBattle",
		UserDefinitions: []*mmpb.UserDefinition{{
			Attributes: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
				"mode": {ValueType: &commonpb.Value_IntegerValue{IntegerValue: mode}},
			}},
		}},
	}
}

func TestCasualBattleMatchmakingPolicy(t *testing.T) {
	if err := validateVioletMatchmakingConfig("tenants/current/matchmakingConfigs/CasualBattle"); err != nil {
		t.Fatal(err)
	}
	if !violetPairConfig("CasualBattle") {
		t.Fatal("CasualBattle must wait for two players")
	}
	if got := publicMatchCapacity("CasualBattle"); got != 2 {
		t.Fatalf("CasualBattle capacity = %d, want 2", got)
	}
	if publicMatchPoolKey(casualBattleTicket(0)) == publicMatchPoolKey(casualBattleTicket(1)) {
		t.Fatal("Singles and Doubles shared a Battle Stadium pool")
	}
}

func TestRankBattleUsesNotificationPasswordPool(t *testing.T) {
	ticket := func(password string) *mmpb.MatchmakingTicket {
		return &mmpb.MatchmakingTicket{
			MatchmakingConfig: "tenants/current/matchmakingConfigs/RankBattle",
			UserDefinitions: []*mmpb.UserDefinition{{
				Attributes: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
					"password": gamesyncStringValue(password),
				}},
			}},
		}
	}
	if err := validateVioletMatchmakingConfig(ticket("pair-key").GetMatchmakingConfig()); err != nil {
		t.Fatal(err)
	}
	if !violetPairConfig("RankBattle") || publicMatchCapacity("RankBattle") != 2 {
		t.Fatal("RankBattle must be a two-player configuration")
	}
	if publicMatchPoolKey(ticket("pair-key")) != publicMatchPoolKey(ticket("pair-key")) {
		t.Fatal("equal Ranked notification keys did not share a pool")
	}
	if publicMatchPoolKey(ticket("pair-key")) == publicMatchPoolKey(ticket("other-key")) {
		t.Fatal("different Ranked notification keys shared a pool")
	}
}

func TestOfficialCompetitionUsesNotificationPasswordPool(t *testing.T) {
	ticket := func(password string) *mmpb.MatchmakingTicket {
		return &mmpb.MatchmakingTicket{
			MatchmakingConfig: "tenants/current/matchmakingConfigs/Competition",
			UserDefinitions: []*mmpb.UserDefinition{{
				Attributes: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
					"password": gamesyncStringValue(password),
				}},
			}},
		}
	}
	if err := validateVioletMatchmakingConfig(ticket("competition-key").GetMatchmakingConfig()); err != nil {
		t.Fatal(err)
	}
	if !violetPairConfig("Competition") || publicMatchCapacity("Competition") != 2 {
		t.Fatal("Competition must be a two-player configuration")
	}
	if publicMatchPoolKey(ticket("competition-key")) != publicMatchPoolKey(ticket("competition-key")) {
		t.Fatal("equal competition notification keys did not share a pool")
	}
	if publicMatchPoolKey(ticket("competition-key")) == publicMatchPoolKey(ticket("other-key")) {
		t.Fatal("different competition notification keys shared a pool")
	}
}
