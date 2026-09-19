package main

import (
	commonpb "npln.nintendo.net/npln-practice/proto/common"
	gspb "npln.nintendo.net/npln-practice/proto/gamesync/v1"
	mmpb "npln.nintendo.net/npln-practice/proto/matchmaking/v1"
	"testing"
)

func TestRoomControlExistsAndOnlyHostCanUpdate(t *testing.T) {
	g := newGamesyncServer(newSessionRegistry())
	gs := &mmpb.GameSession{Name: nplnTenant + "/gameSessions/test", CanParticipate: true, UserSessions: []*mmpb.UserSession{{Name: "userSessions/host"}}}
	if err := g.initializeFixedData(gs); err != nil {
		t.Fatal(err)
	}
	doc := g.documents[gs.Name]["docs/__gs/m"]
	if err := checkPrecondition(&gspb.Precondition{ConditionType: &gspb.Precondition_Exists{Exists: true}}, doc); err != nil {
		t.Fatal(err)
	}
	host := gamesyncSession{GameSession: gs.Name, UserSession: "userSessions/host"}
	for _, key := range []string{"cp", "ip", "ebf"} {
		doc.Fields.Fields[key] = &commonpb.Value{ValueType: &commonpb.Value_BooleanValue{BooleanValue: false}}
	}
	if err := writableDocument(doc.Name, doc.Fields, host, g.owners[gs.Name], doc, nil); err != nil {
		t.Fatal(err)
	}
	guest := host
	guest.UserSession = "userSessions/guest"
	if err := writableDocument(doc.Name, doc.Fields, guest, g.owners[gs.Name], doc, nil); err == nil {
		t.Fatal("guest can close host room")
	}
	if err := g.initializeFixedData(gs); err != nil {
		t.Fatal(err)
	}
	if g.documents[gs.Name][doc.Name].Fields.Fields["cp"].GetBooleanValue() {
		t.Fatal("room admission reset")
	}
}
