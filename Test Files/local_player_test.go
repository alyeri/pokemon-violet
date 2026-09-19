package main

import (
	"encoding/base64"
	authpb "npln.nintendo.net/npln-practice/proto/auth/v1"
	"testing"
)

func TestLocalVioletPlayersRequireOptInAndRemainDistinct(t *testing.T) {
	t.Setenv("NPLN_ALLOW_UNVERIFIED", "1")
	t.Setenv("VIOLET_LOCAL_TWO_PLAYERS", "1")
	token := func(player string) *authpb.ExternalIdToken {
		payload := base64.RawURLEncoding.EncodeToString([]byte(`{"violet_local_player":"` + player + `"}`))
		return &authpb.ExternalIdToken{Token: &authpb.ExternalIdToken_NsaIdToken{NsaIdToken: "e30." + payload + ".local"}}
	}
	for _, player := range []string{"1", "2"} {
		want := uint64(1800000000 + int(player[0]-'0'))
		for i := 0; i < 2; i++ {
			if got, ok := localVioletPlayer(token(player)); !ok || got != want {
				t.Fatalf("player %s: %d %v", player, got, ok)
			}
		}
	}
	if _, ok := localVioletPlayer(token("3")); ok {
		t.Fatal("unexpected player accepted")
	}
	t.Setenv("VIOLET_LOCAL_TWO_PLAYERS", "")
	if _, ok := localVioletPlayer(token("2")); ok {
		t.Fatal("accepted without opt-in")
	}
	t.Setenv("VIOLET_LOCAL_TWO_PLAYERS", "1")
	t.Setenv("NPLN_ALLOW_UNVERIFIED", "")
	if _, ok := localVioletPlayer(token("2")); ok {
		t.Fatal("accepted outside dev mode")
	}
}
