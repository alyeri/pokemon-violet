package main

import (
	"context"
	"regexp"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	commonpb "npln.nintendo.net/npln-practice/proto/common"
	gspb "npln.nintendo.net/npln-practice/proto/gamesync/v1"
	mmpb "npln.nintendo.net/npln-practice/proto/matchmaking/v1"
	ugcpb "npln.nintendo.net/npln-practice/proto/ugcstore/v1"
)

func violetAuthenticatedContext(uid string) context.Context {
	user := nplnTenant + "/users/" + uid
	token := mintNplnAccessToken(1800000001, user, nplnTenant)
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer "+token,
		"uid", uid,
	))
}

func violetTestSession() gamesyncSession {
	return gamesyncSession{
		UID:         "u-violet",
		GameSession: nplnTenant + "/gameSessions/room",
		UserSession: nplnTenant + "/gameSessions/room/userSessions/session-violet",
		Sequence:    1,
		Connection:  1,
	}
}

func TestVioletUserSessionContract(t *testing.T) {
	for _, collection := range []string{"__pus"} {
		doc, ok := observedUserSessionDocument("docs/"+collection+"/session-violet", violetTestSession())
		if !ok {
			t.Fatalf("%s document was not recognized", collection)
		}
		fields := doc.GetFields().GetFields()
		for _, key := range []string{"uid", "ussid", "ucsid", "upcsid", "pgn"} {
			if fields[key] == nil {
				t.Fatalf("%s.%s is missing", collection, key)
			}
		}
		if len(fields) != 5 {
			t.Fatalf("%s has unexpected fields: %v", collection, fields)
		}
	}
}

func TestVioletRankedUserSessionContract(t *testing.T) {
	session := violetTestSession()
	session.Team = "main"
	session.Attributes = &commonpb.MapValue{Fields: map[string]*commonpb.Value{"rank": gamesyncIntegerValue(1)}}
	session.LatencyJSON = `{"latencies":{}}`
	server := newGamesyncServer()
	server.rememberSession(session)
	doc := server.documents[session.GameSession]["docs/__us/session-violet"]
	fields := doc.GetFields().GetFields()
	for _, key := range []string{"uid", "st", "att", "ltc", "tn", "ussid", "ucsid"} {
		if fields[key] == nil {
			t.Errorf("__us.%s missing", key)
		}
	}
	// main+0x1e7074c accepts a participant only if tn == "main" and
	// ussid != 0; otherwise neither local nor remote readiness is set.
	if fields["tn"].GetStringValue() != "main" || fields["ussid"].GetIntegerValue() == 0 {
		t.Error("Ranked participant cannot pass the native readiness predicate")
	}
	if fields["st"].GetIntegerValue() != int64(mmpb.UserSession_ACTIVE) || fields["ltc"].GetStringValue() != session.LatencyJSON {
		t.Error("session state or latency not preserved")
	}
	if fields["att"].GetMapValue().GetFields()["rank"].GetIntegerValue() != 1 {
		t.Error("typed matchmaking attributes not preserved")
	}
	if len(fields) != 7 || fields["upcsid"] != nil || fields["pgn"] != nil {
		t.Error("__us must not reuse the __pus schema")
	}
}

func TestVioletRankedBothParticipantsBecomeDiscoverable(t *testing.T) {
	server := newGamesyncServer()
	first := violetTestSession()
	first.Team = "main"
	second := first
	second.UID = "u-opponent"
	second.UserSession = first.GameSession + "/userSessions/opponent"
	server.rememberSession(first)
	server.rememberSession(second)
	server.rememberSession(first) // Token refresh must retain identity and schema.
	for _, local := range []gamesyncSession{first, second} {
		localSequence := server.sessions[lastResourceSegment(local.UserSession)].Sequence
		ownReady, peerReady := false, false
		for _, doc := range server.documents[first.GameSession] {
			f := doc.GetFields().GetFields()
			sequence := f["ussid"].GetIntegerValue()
			if f["tn"].GetStringValue() != "main" || sequence == 0 {
				continue
			}
			if sequence == localSequence {
				ownReady = true
			} else {
				peerReady = true
			}
		}
		if !ownReady || !peerReady {
			t.Fatalf("%s: Ranked __us readiness own=%v peer=%v", local.UID, ownReady, peerReady)
		}
	}
}

func TestVioletRankedBattleControlDocuments(t *testing.T) {
	session := violetTestSession()
	owners := map[string]string{}
	sessions := map[string]gamesyncSession{lastResourceSegment(session.UserSession): session}
	startName := "docs/@@battleStart/id"
	startFields := &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"competitionId": gamesyncStringValue("local-ranked-singles-1"),
		"users": {ValueType: &commonpb.Value_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: []*commonpb.Value{
			gamesyncIntegerValue(1), gamesyncIntegerValue(2),
		}}}},
	}}
	if err := writableDocument(startName, startFields, session, owners, nil, sessions); err != nil {
		t.Fatalf("observed battle-start rejected: %v", err)
	}
	owners[startName] = lastResourceSegment(session.UserSession)
	if err := writableDocument(startName, startFields, session, owners, &gspb.Document{Name: startName}, sessions); err != nil {
		t.Fatalf("owner battle-start update rejected: %v", err)
	}

	reportName := "docs/@@battleReport/id/users/1"
	reportFields := &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"competitionId": gamesyncStringValue("local-ranked-singles-1"),
		"behavior":      gamesyncMapValue(&commonpb.MapValue{Fields: map[string]*commonpb.Value{"1": gamesyncIntegerValue(0), "2": gamesyncIntegerValue(0)}}),
		"result":        gamesyncMapValue(&commonpb.MapValue{Fields: map[string]*commonpb.Value{"1": gamesyncIntegerValue(0), "2": gamesyncIntegerValue(1)}}),
		"log":           gamesyncBytesValue([]byte{1, 2, 3}),
	}}
	if err := writableDocument(reportName, reportFields, session, owners, nil, sessions); err != nil {
		t.Fatalf("observed battle report rejected: %v", err)
	}
	if err := writableDocument("docs/@@battleReport/id/users/2", reportFields, session, owners, nil, sessions); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign battle report accepted: %v", err)
	}
}

func TestVioletSharedStateContract(t *testing.T) {
	doc, ok := violetStateDocument("docs/__stg/All", violetTestSession())
	if !ok {
		t.Fatal("__stg/All was not recognized")
	}
	fields := doc.GetFields().GetFields()
	for _, key := range []string{"suid", "susid", "sussid", "suscid", "pl", "mp"} {
		if fields[key] == nil {
			t.Fatalf("__stg.%s is missing", key)
		}
	}
	if fields["susid"].GetStringValue() != "session-violet" {
		t.Fatalf("susid=%q", fields["susid"].GetStringValue())
	}
	if _, ok := fields["pl"].GetValueType().(*commonpb.Value_BytesValue); !ok {
		t.Fatal("pl must be bytes")
	}
	if _, ok := fields["mp"].GetValueType().(*commonpb.Value_MapValue); !ok {
		t.Fatal("mp must be a map")
	}
}

func TestVioletDoesNotSynthesizeSignallingMailboxes(t *testing.T) {
	server := newGamesyncServer()
	server.rememberSession(violetTestSession())

	documents := server.documents[violetTestSession().GameSession]
	if documents["docs/__us/session-violet"] == nil {
		t.Fatal("server-owned user session documents were not initialized")
	}
	if documents["docs/__pus/session-violet"] != nil {
		t.Fatal("client-owned presence must not be synthesized")
	}
	if documents["docs/__stu/session-violet"] != nil || documents["docs/__stg/All"] != nil {
		t.Fatal("empty participant signalling mailboxes must not be synthesized")
	}
	for _, name := range []string{"docs/__stu/session-violet", "docs/__stg/All"} {
		target := &gspb.Target{TargetType: &gspb.Target_Documents{Documents: &gspb.DocumentsTarget{Documents: []string{name}}}}
		if snapshot := server.targetSnapshot(violetTestSession(), target); len(snapshot) != 0 {
			t.Fatalf("empty signalling target %s returned %d fabricated documents", name, len(snapshot))
		}
	}
}

func TestVioletPresenceAllocatesIndependentPeerConnectionID(t *testing.T) {
	server := newGamesyncServer()
	session := violetTestSession()
	server.rememberSession(session)
	peer := session
	peer.UID = "u-peer"
	peer.UserSession = session.GameSession + "/userSessions/peer"
	server.rememberSession(peer)
	if server.documents[session.GameSession]["docs/__pus/peer"] != nil {
		t.Fatal("unpublished peer has a provisional PIA identity")
	}
	stored, ok := server.sessionByID("session-violet")
	if !ok {
		t.Fatal("remembered session is missing")
	}
	token := mintSessionToken(stored.UID, nplnTenant, stored.GameSession, stored.UserSession)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	name := "docs/__pus/session-violet"

	_, err := server.WriteDocuments(ctx, &gspb.WriteDocumentsRequest{WriteOperations: []*gspb.WriteOperation{
		{OperationType: &gspb.WriteOperation_UpdateDocument{UpdateDocument: &gspb.UpdateDocumentRequest{
			Document: &gspb.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
				"uid": gamesyncStringValue(stored.UID), "ussid": gamesyncIntegerValue(stored.Sequence),
				"ucsid": gamesyncIntegerValue(stored.Connection), "upcsid": gamesyncIntegerValue(0),
				"pgn": gamesyncStringValue("All"),
			}}},
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"*"}},
		}}},
		{OperationType: &gspb.WriteOperation_TransformDocument{TransformDocument: &gspb.TransformDocumentRequest{
			Name: name,
			FieldTransforms: []*gspb.FieldTransform{
				{FieldPath: "`upcsid`", TransformType: &gspb.FieldTransform_LoadGlobalValue{LoadGlobalValue: "__upcsidn"}},
				{FieldPath: "`upcsid`", TransformType: &gspb.FieldTransform_Maximum{Maximum: gamesyncIntegerValue(20000)}},
				{FieldPath: "`upcsid`", TransformType: &gspb.FieldTransform_Increment{Increment: gamesyncIntegerValue(1)}},
				{FieldPath: "`upcsid`", TransformType: &gspb.FieldTransform_StoreGlobalValue{StoreGlobalValue: "__upcsidn"}},
			},
		}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := server.documents[stored.GameSession][name].GetFields().GetFields()["upcsid"].GetIntegerValue(); got != 20001 {
		t.Fatalf("upcsid=%d, want independently allocated 20001", got)
	}
	if got := server.sessions["session-violet"].Connection; got != 1 {
		t.Fatalf("ucsid/suscid connection sequence was overwritten with upcsid: %d", got)
	}
	server.rememberSession(session)
	if got := server.documents[stored.GameSession][name].Fields.Fields["upcsid"].GetIntegerValue(); got != 20001 {
		t.Fatalf("token refresh overwrote published peer identity: %d", got)
	}
	target := &gspb.Target{TargetType: &gspb.Target_Collection{Collection: &gspb.CollectionTarget{Collection: "docs/__pus"}}}
	snapshot := server.targetSnapshot(stored, target)
	if len(snapshot) != 1 || snapshot[0].Name != name {
		t.Fatal("presence snapshot includes unready peer")
	}
}

func TestVioletTenantConstants(t *testing.T) {
	if nplnAppID != "01008F6008C5E000" || nplnTenantID != "t-50e39f8f-lp1" {
		t.Fatalf("wrong Violet identity: app=%q tenant=%q", nplnAppID, nplnTenantID)
	}
}

func TestVioletObservedCertificateCoversBothServices(t *testing.T) {
	dnsNames, ipAddresses := certificateProfileSANs("observed")
	if len(ipAddresses) != 0 {
		t.Fatalf("observed profile unexpectedly contains IP SANs: %v", ipAddresses)
	}
	if len(dnsNames) != 2 || dnsNames[0] != violetTLSHostname || dnsNames[1] != violetGamesyncHost {
		t.Fatalf("observed certificate SANs=%v", dnsNames)
	}
}

func TestVioletRejectsForeignGamesyncFamilies(t *testing.T) {
	session := violetTestSession()
	for _, name := range []string{"docs/c/abcdef1234567890", "docs/p/1", "docs/e/1", "docs/__bl/session-violet"} {
		err := writableDocument(name, &commonpb.MapValue{Fields: map[string]*commonpb.Value{}}, session, map[string]string{}, nil, map[string]gamesyncSession{})
		if status.Code(err) != codes.Unimplemented {
			t.Fatalf("%s: expected Unimplemented, got %v", name, err)
		}
	}
}

func TestVioletUnionCircleCreationAndAlias(t *testing.T) {
	ctx := violetAuthenticatedContext("u-violet")
	registry := newSessionRegistry()
	server := newGameSessionServer(registry)
	ticket, err := server.CreateGameSessionCreationTicket(ctx, &mmpb.CreateGameSessionCreationTicketRequest{
		GameSessionCreationTicket: &mmpb.GameSessionCreationTicket{
			MatchmakingConfig: violetUnionCircleConfig,
			UserDefinitions:   []*mmpb.UserDefinition{{User: "users/current"}},
			GameSession:       &mmpb.GameSession{Password: "TeamCircle"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, ok := server.completeGameSessionCreationTicket(ctx, ticket.GetName(), []string{"users/current"})
	if !ok {
		t.Fatal("ticket did not complete")
	}
	gs := completed.GetGameSession()
	uuidPattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !uuidPattern.MatchString(lastResourceSegment(gs.GetName())) {
		t.Fatalf("game session does not use a UUID: %q", gs.GetName())
	}
	if len(gs.GetUserSessions()) != 1 || !uuidPattern.MatchString(lastResourceSegment(gs.GetUserSessions()[0].GetName())) {
		t.Fatalf("user session does not use a UUID: %v", gs.GetUserSessions())
	}
	if gs.GetHost() != "127.0.0.1" || gs.GetPort() != 443 || gs.GetMaxParticipantCount() != 4 {
		t.Fatalf("unexpected Union Circle endpoint/capacity: %s:%d max=%d", gs.GetHost(), gs.GetPort(), gs.GetMaxParticipantCount())
	}
	if gs.GetProperties().GetFields()["_AliasSuffix"] != nil || gs.GetProperties().GetFields()["_BaseConfigName"] != nil {
		t.Fatal("foreign title metadata leaked into Violet session properties")
	}
	alias, err := server.CreateGameSessionShortAlias(ctx, &mmpb.CreateGameSessionShortAliasRequest{
		GameSessionShortAlias: &mmpb.GameSessionShortAlias{GameSession: gs.GetName()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if code := lastResourceSegment(alias.GetName()); !validVioletRoomCode(code) {
		t.Fatalf("invalid Violet alias code %q", code)
	}
	// Violet 3.0.1 main+0x4f6fd0 compares this collection case-sensitively.
	// A lower-case g causes the constructor at main+0x4f7350 to abort.
	wantAliasName := nplnTenant + "/GameSessionShortAliases/" + lastResourceSegment(alias.GetName())
	if alias.GetName() != wantAliasName {
		t.Fatalf("alias resource rejected by Violet parser: got %q want %q", alias.GetName(), wantAliasName)
	}
	resolved, err := server.GetGameSessionShortAlias(ctx, &mmpb.GetGameSessionShortAliasRequest{Name: alias.GetName()})
	if err != nil || resolved.GetGameSession() != gs.GetName() {
		t.Fatalf("alias resolution failed: resolved=%v err=%v", resolved, err)
	}

	if len(completed.GetMatchedUserSessions()) != 1 || completed.GetMatchedUserSessions()[0].GetMatchmakingIdToken() == "" {
		t.Fatal("creation ticket did not return the host Gamesync token")
	}
	matched := completed.GetMatchedUserSessions()[0]
	gamesync := newGamesyncServer(registry)
	issued, err := gamesync.IssueToken(context.Background(), &gspb.IssueTokenRequest{
		UserSession:        "userSessions/" + lastResourceSegment(matched.GetUserSession()),
		MatchmakingIdToken: matched.GetMatchmakingIdToken(),
	})
	if err != nil {
		t.Fatalf("Gamesync IssueToken failed against the shared registry: %v", err)
	}
	gamesyncCtx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+issued.GetToken().GetAccessToken()))
	doc, err := gamesync.GetDocument(gamesyncCtx, &gspb.GetDocumentRequest{Name: "docs/__gs/f"})
	if err != nil || doc.GetFields().GetFields()["mcn"].GetStringValue() != violetUnionCircleConfig {
		t.Fatalf("shared Gamesync document unavailable: doc=%v err=%v", doc, err)
	}
}

func TestVioletPenaltyDocumentContract(t *testing.T) {
	ctx := violetAuthenticatedContext("u-violet")
	// The UGC owner is intentionally distinct from the bearer subject, matching
	// the locally observed Violet request.
	name := "tenants/current/documents/users/u-qohncazuuo3scwldqfpm/privateItems/penalty"
	doc, err := (&violetUgcstoreServer{}).GetDocument(ctx, &ugcpb.GetDocumentRequest{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	fields := doc.GetFields().GetFields()
	if len(fields) != 3 || fields["disconnect_count"].GetIntegerValue() != 0 || fields["penalty_start_at"].GetIntegerValue() != 0 || fields["warning_flag"].GetBooleanValue() {
		t.Fatalf("unexpected penalty document: %v", fields)
	}
	if _, err := (&violetUgcstoreServer{}).GetDocument(ctx, &ugcpb.GetDocumentRequest{Name: "tenants/current/documents/course"}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("foreign UGC path was accepted: %v", err)
	}
}

func TestVioletGameSessionMutableContract(t *testing.T) {
	registry := newSessionRegistry()
	server := newGamesyncServer(registry)
	gs := &mmpb.GameSession{Name: nplnTenant + "/gameSessions/room", Host: "127.0.0.1", Port: 443, MaxParticipantCount: 4}
	if err := server.initializeFixedData(gs); err != nil {
		t.Fatal(err)
	}
	doc := server.documents[gs.GetName()]["docs/__gs/f"]
	if doc == nil {
		t.Fatal("missing docs/__gs/f")
	}
	fields := doc.GetFields().GetFields()
	for _, key := range []string{"gsid", "addr", "p", "mcn", "maxu", "rs"} {
		if fields[key] == nil {
			t.Fatalf("__gs/f.%s missing", key)
		}
	}
	if fields["mcn"].GetStringValue() != violetUnionCircleConfig {
		t.Fatalf("mcn=%q", fields["mcn"].GetStringValue())
	}
	if _, ok := fields["rs"].GetValueType().(*commonpb.Value_BytesValue); !ok {
		t.Fatal("rs must be bytes")
	}
	if len(fields) != 6 {
		t.Fatalf("__gs/f has non-evidenced fields: %v", fields)
	}
}

func TestVioletIceServerSetIsCanonicalAndComplete(t *testing.T) {
	server := newGameSessionServer()
	set, err := server.AllocateIceServerSet(context.Background(), &mmpb.AllocateIceServerSetRequest{
		Tenant: "tenants/current",
	})
	if err != nil {
		t.Fatal(err)
	}
	if set.GetName() != nplnTenant+"/iceServerSets/default" {
		t.Fatalf("non-canonical ICE name: %q", set.GetName())
	}
	if set.GetStunServer() == nil || len(set.GetTurnServers()) == 0 {
		t.Fatal("ICE response must contain both STUN and TURN endpoints")
	}
	if set.GetTtl() == nil || set.GetUpdateTime() == nil || set.GetClientCacheDuration() == nil {
		t.Fatalf("incomplete ICE lifetime metadata: %v", set)
	}
	if _, err := server.AllocateIceServerSet(context.Background(), &mmpb.AllocateIceServerSetRequest{
		Tenant: "tenants/t-wrong",
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("foreign tenant was accepted: %v", err)
	}
}
