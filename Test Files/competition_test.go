package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
)

func testStringField(t *testing.T, data []byte, target protowire.Number) string {
	t.Helper()
	for len(data) > 0 {
		number, typ, tagSize := protowire.ConsumeTag(data)
		if tagSize < 0 {
			t.Fatal(protowire.ParseError(tagSize))
		}
		data = data[tagSize:]
		if number == target && typ == protowire.BytesType {
			value, consumed := protowire.ConsumeString(data)
			if consumed < 0 {
				t.Fatal(protowire.ParseError(consumed))
			}
			return value
		}
		consumed := protowire.ConsumeFieldValue(number, typ, data)
		if consumed < 0 {
			t.Fatal(protowire.ParseError(consumed))
		}
		data = data[consumed:]
	}
	return ""
}

func testVarintField(t *testing.T, data []byte, target protowire.Number) uint64 {
	t.Helper()
	var found uint64
	for len(data) > 0 {
		number, typ, tagSize := protowire.ConsumeTag(data)
		if tagSize < 0 {
			t.Fatal(protowire.ParseError(tagSize))
		}
		data = data[tagSize:]
		if number == target && typ == protowire.VarintType {
			value, consumed := protowire.ConsumeVarint(data)
			if consumed < 0 {
				t.Fatal(protowire.ParseError(consumed))
			}
			found = value
		}
		consumed := protowire.ConsumeFieldValue(number, typ, data)
		if consumed < 0 {
			t.Fatal(protowire.ParseError(consumed))
		}
		data = data[consumed:]
	}
	return found
}

func TestHostCompetitionPreservesFriendlyPayload(t *testing.T) {
	server := newCompetitionServer()
	competition := appendStringField(nil, 1, "tenants/current/competitions/")
	applicationData := appendStringField(nil, 1, "RulePresetNo=10")
	competition = appendBytesField(competition, 24, applicationData)
	competition = appendVarintField(competition, 33, 2)
	participant := appendVarintField(nil, 2, 51)
	participant = appendStringField(participant, 3, "device-uuid")
	participant = appendStringField(participant, 4, "+0000")
	request := appendStringField(nil, 1, "tenants/current")
	request = appendBytesField(request, 2, competition)
	request = appendBytesField(request, 3, participant)

	response, err := server.HostCompetition(violetAuthenticatedContext("u-host"), &rawMsg{b: request})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.b) == 0 {
		t.Fatal("HostCompetition returned an empty response")
	}

	server.mu.Lock()
	profile := server.users["u-host"]
	hosted, ok := server.hosted[profile.currentFriendly]
	server.mu.Unlock()
	name := profile.currentFriendly
	if !ok {
		t.Fatalf("hosted competition %q was not stored", name)
	}
	if !strings.HasPrefix(name, nplnTenant+"/competitions/") || strings.HasSuffix(name, "/") {
		t.Fatalf("server did not allocate a competition resource ID: %q", name)
	}
	storedName, competitionType, err := competitionIdentity(hosted.competition)
	if err != nil {
		t.Fatal(err)
	}
	if storedName != name || competitionType != 2 {
		t.Fatalf("unexpected hosted identity: name=%q type=%d", storedName, competitionType)
	}
	if !bytes.Contains(hosted.competition, applicationData) {
		t.Fatal("client-supplied application_data was not preserved")
	}
	aliasName := testStringField(t, hosted.competition, 2)
	aliasCode := lastResourceSegment(aliasName)
	if !strings.HasPrefix(aliasName, nplnTenant+"/competitionAliases/") || len(aliasCode) != 6 {
		t.Fatalf("invalid friendly competition alias %q", aliasName)
	}
	aliasResponse, err := server.GetCompetitionAlias(violetAuthenticatedContext("u-guest"), &rawMsg{b: appendStringField(nil, 1, strings.Replace(aliasName, nplnTenant, "tenants/current", 1))})
	if err != nil {
		t.Fatal(err)
	}
	if gotAlias, gotCompetition := testStringField(t, aliasResponse.b, 1), testStringField(t, aliasResponse.b, 2); gotAlias != aliasName || gotCompetition != name {
		t.Fatalf("unexpected competition alias response: alias=%q competition=%q", gotAlias, gotCompetition)
	}
	if !bytes.Contains(hosted.participant, []byte(name+"/participants/u-host")) ||
		!bytes.Contains(hosted.participant, []byte("device-uuid")) {
		t.Fatal("participant identity or client data was not preserved")
	}
	if profile.currentFriendly != name {
		t.Fatalf("profile did not reference hosted competition: %q", profile.currentFriendly)
	}

	got, err := server.GetCompetition(violetAuthenticatedContext("u-guest"), &rawMsg{b: appendStringField(nil, 1, name)})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.b, hosted.competition) {
		t.Fatal("GetCompetition did not return the stored competition")
	}

	requestedParticipant := strings.Replace(name, nplnTenant, "tenants/current", 1) + "/participants/u-game"
	gotParticipant, err := server.GetCompetitionParticipant(violetAuthenticatedContext("u-host"), &rawMsg{b: appendStringField(nil, 1, requestedParticipant)})
	if err != nil {
		t.Fatal(err)
	}
	participantResource, err := parseProtoStringMessage(gotParticipant.b)
	if err != nil {
		t.Fatal(err)
	}
	wantParticipant := name + "/participants/u-game"
	if participantResource != wantParticipant {
		t.Fatalf("unexpected hosted participant resource: got %q want %q", participantResource, wantParticipant)
	}
}

func TestCompetitionUserWireContract(t *testing.T) {
	const name = "tenants/current/competitionUsers/current"
	wire := marshalCompetitionUser(name)

	seenName := ""
	seenLanguage := ""
	seenCategory := uint64(0)
	seenBattleStats := false
	seenSingles := ""
	seenDoubles := ""
	seenCreateTime := false
	for len(wire) > 0 {
		number, typ, n := protowire.ConsumeTag(wire)
		if n < 0 {
			t.Fatal(protowire.ParseError(n))
		}
		wire = wire[n:]
		switch number {
		case 1, 4, 15, 17:
			if typ != protowire.BytesType {
				t.Fatalf("field %d has type %d", number, typ)
			}
			value, consumed := protowire.ConsumeString(wire)
			if consumed < 0 {
				t.Fatal(protowire.ParseError(consumed))
			}
			if number == 1 {
				seenName = value
			} else if number == 4 {
				seenLanguage = value
			} else if number == 15 {
				seenSingles = value
			} else {
				seenDoubles = value
			}
			n = consumed
		case 13, 19:
			if typ != protowire.BytesType {
				t.Fatalf("field %d has type %d", number, typ)
			}
			_, consumed := protowire.ConsumeBytes(wire)
			if consumed < 0 {
				t.Fatal(protowire.ParseError(consumed))
			}
			if number == 13 {
				seenBattleStats = true
			} else {
				seenCreateTime = true
			}
			n = consumed
		case 14:
			if typ != protowire.VarintType {
				t.Fatalf("category has type %d", typ)
			}
			value, consumed := protowire.ConsumeVarint(wire)
			if consumed < 0 {
				t.Fatal(protowire.ParseError(consumed))
			}
			seenCategory, n = value, consumed
		default:
			t.Fatalf("unexpected field %d", number)
		}
		wire = wire[n:]
	}

	if seenName != name || seenLanguage != "en" || seenCategory != 3 || !seenBattleStats || !seenCreateTime || seenSingles != rankedCompetitionName(3) || seenDoubles != rankedCompetitionName(4) {
		t.Fatalf("unexpected competition user: name=%q language=%q category=%d stats=%v create=%v singles=%q doubles=%q", seenName, seenLanguage, seenCategory, seenBattleStats, seenCreateTime, seenSingles, seenDoubles)
	}
}

func TestUpdateCompetitionUserCapturedOnlineCompetitionProfile(t *testing.T) {
	user := appendStringField(nil, 1, "tenants/current/competitionUsers/current")
	user = appendVarintField(user, 3, 399)
	user = appendStringField(user, 4, "en")
	user = appendVarintField(user, 14, 1)
	mask := []byte(nil)
	for _, path := range []string{"category", "birthmonth", "area", "language_code"} {
		mask = appendStringField(mask, 1, path)
	}
	request := appendBytesField(nil, 1, user)
	request = appendBytesField(request, 2, mask)

	server := newCompetitionServer()
	response, err := server.UpdateCompetitionUser(violetAuthenticatedContext("u-junior"), &rawMsg{b: request})
	if err != nil {
		t.Fatal(err)
	}
	profile := server.users["u-junior"]
	if profile.area != 399 || profile.language != "en" || profile.category != 1 || !profile.initialized {
		t.Fatalf("unexpected stored profile: %#v", profile)
	}

	parsed, err := parseCompetitionUserUpdate(appendBytesField(nil, 1, response.b))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.name != "tenants/current/competitionUsers/current" || parsed.area != 399 || parsed.language != "en" || parsed.category != 1 {
		t.Fatalf("unexpected UpdateCompetitionUser response: %#v", parsed)
	}

	getResponse, err := server.GetCompetitionUser(violetAuthenticatedContext("u-junior"), &rawMsg{b: appendStringField(nil, 1, "tenants/current/competitionUsers/current")})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err = parseCompetitionUserUpdate(appendBytesField(nil, 1, getResponse.b))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.area != 399 || parsed.category != 1 {
		t.Fatalf("GetCompetitionUser lost saved profile: %#v", parsed)
	}
}

func TestRankedCompetitionSearchWireContract(t *testing.T) {
	request := appendStringField(nil, 1, "tenants/current")
	request = appendVarintField(request, 2, 2)
	request = appendVarintField(request, 4, 1)
	request = appendBytesField(request, 6, []byte{3, 4})
	parsed, err := parseSearchCompetitionsRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.pageSize != 2 || parsed.view != 1 || len(parsed.types) != 2 || parsed.types[0] != 3 || parsed.types[1] != 4 {
		t.Fatalf("unexpected ranked search: %#v", parsed)
	}

	response, err := newCompetitionServer().SearchCompetitions(violetAuthenticatedContext("u-owner"), &rawMsg{b: request})
	if err != nil {
		t.Fatal(err)
	}
	remaining := response.b
	seen := []uint64{}
	for len(remaining) > 0 {
		number, typ, outerTagSize := protowire.ConsumeTag(remaining)
		if outerTagSize < 0 || number != 1 || typ != protowire.BytesType {
			t.Fatalf("unexpected search response field %d type %d", number, typ)
		}
		competition, consumed := protowire.ConsumeBytes(remaining[outerTagSize:])
		if consumed < 0 {
			t.Fatal(protowire.ParseError(consumed))
		}
		var competitionType uint64
		for len(competition) > 0 {
			field, fieldType, fieldTagSize := protowire.ConsumeTag(competition)
			if fieldTagSize < 0 {
				t.Fatal(protowire.ParseError(fieldTagSize))
			}
			competition = competition[fieldTagSize:]
			var fieldSize int
			if field == 33 && fieldType == protowire.VarintType {
				competitionType, fieldSize = protowire.ConsumeVarint(competition)
			} else {
				fieldSize = protowire.ConsumeFieldValue(field, fieldType, competition)
			}
			if fieldSize < 0 {
				t.Fatal(protowire.ParseError(fieldSize))
			}
			competition = competition[fieldSize:]
		}
		seen = append(seen, competitionType)
		remaining = remaining[outerTagSize+consumed:]
	}
	if len(seen) != 2 || seen[0] != 3 || seen[1] != 4 {
		t.Fatalf("ranked competition types = %v", seen)
	}
}

func TestRankedCompetitionSearchRejectsOtherTypes(t *testing.T) {
	request := appendStringField(nil, 1, "tenants/current")
	request = appendVarintField(request, 2, 1)
	request = appendVarintField(request, 4, 1)
	request = appendBytesField(request, 6, []byte{5})
	_, err := newCompetitionServer().SearchCompetitions(violetAuthenticatedContext("u-owner"), &rawMsg{b: request})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("ranked search with type 5 error = %v, want InvalidArgument", err)
	}
}

func TestOnlineOfficialCompetitionCapturedSearch(t *testing.T) {
	image := filepath.Join(t.TempDir(), "main.bin")
	f, err := os.Create(image)
	if err != nil {
		t.Fatal(err)
	}
	record := make([]byte, violetRegulationSize)
	copy(record, []byte{1, 3, 6, 3, 3})
	record[len(record)-1] = 0xa5
	if _, err := f.WriteAt(record, violetRegulationOffset+9*violetRegulationSize); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIOLET_RANKED_MAIN_IMAGE", image)

	request := appendStringField(nil, 1, "tenants/current")
	request = appendVarintField(request, 2, 10)
	request = appendVarintField(request, 4, 1)
	request = appendBytesField(request, 6, []byte{1})

	response, err := newCompetitionServer().SearchCompetitions(violetAuthenticatedContext("u-owner"), &rawMsg{b: request})
	if err != nil {
		t.Fatal(err)
	}
	competition, consumed := protowire.ConsumeBytes(response.b[1:])
	if consumed < 0 {
		t.Fatal(protowire.ParseError(consumed))
	}
	var name string
	var alias string
	var matchmakingConfig string
	var competitionType uint64
	var applicationData []byte
	for len(competition) > 0 {
		field, typ, tagSize := protowire.ConsumeTag(competition)
		if tagSize < 0 {
			t.Fatal(protowire.ParseError(tagSize))
		}
		competition = competition[tagSize:]
		var fieldSize int
		if field == 1 && typ == protowire.BytesType {
			name, fieldSize = protowire.ConsumeString(competition)
		} else if field == 2 && typ == protowire.BytesType {
			alias, fieldSize = protowire.ConsumeString(competition)
		} else if field == 17 && typ == protowire.BytesType {
			matchmakingConfig, fieldSize = protowire.ConsumeString(competition)
		} else if field == 24 && typ == protowire.BytesType {
			applicationData, fieldSize = protowire.ConsumeBytes(competition)
		} else if field == 33 && typ == protowire.VarintType {
			competitionType, fieldSize = protowire.ConsumeVarint(competition)
		} else {
			fieldSize = protowire.ConsumeFieldValue(field, typ, competition)
		}
		if fieldSize < 0 {
			t.Fatal(protowire.ParseError(fieldSize))
		}
		competition = competition[fieldSize:]
	}
	if name != officialCompetitionName() || competitionType != 1 || matchmakingConfig != nplnTenant+"/matchmakingConfigs/Competition" {
		t.Fatalf("unexpected official competition name=%q type=%d matchmaking_config=%q", name, competitionType, matchmakingConfig)
	}
	if alias != competitionAliasName("OFC001") {
		t.Fatalf("unexpected official competition alias %q", alias)
	}
	for _, key := range []string{"RulePresetNo", "ControlTimeOverride", "HostPlayerName", "TotalTimeOverride", "Regulation"} {
		if !bytes.Contains(applicationData, []byte(key)) {
			t.Fatalf("official application_data is missing %q", key)
		}
	}
}

func TestGetOnlineOfficialCompetitionAcceptsCurrentTenantAlias(t *testing.T) {
	image := filepath.Join(t.TempDir(), "main.bin")
	f, err := os.Create(image)
	if err != nil {
		t.Fatal(err)
	}
	record := make([]byte, violetRegulationSize)
	copy(record, []byte{1, 3, 6, 3, 3})
	if _, err := f.WriteAt(record, violetRegulationOffset+9*violetRegulationSize); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIOLET_RANKED_MAIN_IMAGE", image)

	request := appendStringField(nil, 1, "tenants/current/competitions/local-online-official-1")
	request = appendVarintField(request, 2, 1) // FULL.
	response, err := newCompetitionServer().GetCompetition(violetAuthenticatedContext("u-owner"), &rawMsg{b: request})
	if err != nil {
		t.Fatal(err)
	}
	if got := testStringField(t, response.b, 1); got != officialCompetitionName() {
		t.Fatalf("official competition name = %q, want %q", got, officialCompetitionName())
	}
}

func TestCreateOfficialCompetitionParticipant(t *testing.T) {
	server := newCompetitionServer()
	uid := "u-owner"
	server.users[uid] = competitionUserProfile{area: 399, language: "en", category: 1, initialized: true}
	binary := bytes.Repeat([]byte{0x5a}, 2352)
	participant := appendVarintField(nil, 2, 51)
	participant = appendStringField(participant, 3, "78563412-dea1-71a5-016d-7342f0debc9a")
	participant = appendStringField(participant, 4, "+0000")
	participant = appendBytesField(participant, 5, binary)
	request := appendStringField(nil, 1, "tenants/current/competitions/local-online-official-1")
	request = appendBytesField(request, 2, participant)

	response, err := server.CreateCompetitionParticipant(violetAuthenticatedContext(uid), &rawMsg{b: request})
	if err != nil {
		t.Fatal(err)
	}
	wantName := officialCompetitionName() + "/participants/" + uid
	if got := testStringField(t, response.b, 1); got != wantName {
		t.Fatalf("participant name = %q, want %q", got, wantName)
	}
	if !bytes.Contains(response.b, binary) {
		t.Fatal("participant response did not preserve participant_binary")
	}
	if got := testVarintField(t, response.b, 13); got != rankedInitialRating {
		t.Fatalf("participant rating = %d, want %d", got, rankedInitialRating)
	}

	userResponse, err := server.GetCompetitionUser(violetAuthenticatedContext(uid), &rawMsg{b: appendStringField(nil, 1, "tenants/current/competitionUsers/current")})
	if err != nil {
		t.Fatal(err)
	}
	if got := testStringField(t, userResponse.b, 5); got != officialCompetitionName() {
		t.Fatalf("current official competition = %q", got)
	}

	participantResponse, err := server.GetCompetitionParticipant(violetAuthenticatedContext(uid), &rawMsg{b: appendStringField(nil, 1, wantName)})
	if err != nil {
		t.Fatal(err)
	}
	if got := testStringField(t, participantResponse.b, 1); got != wantName {
		t.Fatalf("stored participant name = %q", got)
	}

	requestedAlias := officialCompetitionName() + "/participants/u-title-save"
	aliasResponse, err := server.GetCompetitionParticipant(violetAuthenticatedContext(uid), &rawMsg{b: appendStringField(nil, 1, requestedAlias)})
	if err != nil {
		t.Fatal(err)
	}
	if got := testStringField(t, aliasResponse.b, 1); got != requestedAlias {
		t.Fatalf("aliased participant name = %q, want %q", got, requestedAlias)
	}
	if !bytes.Contains(aliasResponse.b, binary) {
		t.Fatal("aliased participant response did not preserve participant_binary")
	}

	activateRequest := appendStringField(nil, 1, "tenants/current/competitions/local-online-official-1/participants/current")
	activateRequest = appendBytesField(activateRequest, 2, nil)
	activated, err := server.ActivateCompetitionParticipant(violetAuthenticatedContext(uid), &rawMsg{b: activateRequest})
	if err != nil {
		t.Fatal(err)
	}
	if got := testVarintField(t, activated.b, 6); got != 2 {
		t.Fatalf("activated participant state = %d, want ACTIVE", got)
	}
	if !bytes.Contains(activated.b, binary) {
		t.Fatal("activation did not preserve participant_binary")
	}
	if got := testVarintField(t, activated.b, 13); got != rankedInitialRating {
		t.Fatalf("activated participant rating = %d, want %d", got, rankedInitialRating)
	}
	if competition, err := rankedNotificationCompetition("tenants/current/competitions/local-online-official-1/participants/current/notification"); err != nil || competition != officialCompetitionName() {
		t.Fatalf("official notification competition = %q, error=%v", competition, err)
	}
}

func TestRankedCompetitionEmptyDiagnosticMode(t *testing.T) {
	t.Setenv("VIOLET_RANKED_DISCOVERY_MODE", "empty")
	request := appendStringField(nil, 1, "tenants/current")
	request = appendVarintField(request, 2, 2)
	request = appendVarintField(request, 4, 1)
	request = appendBytesField(request, 6, []byte{3, 4})
	response, err := newCompetitionServer().SearchCompetitions(violetAuthenticatedContext("u-owner"), &rawMsg{b: request})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.b) != 0 {
		t.Fatalf("diagnostic response has %d bytes, want 0", len(response.b))
	}
}

func TestRankedCompetitionScheduledDiscoveryMode(t *testing.T) {
	t.Setenv("VIOLET_RANKED_DISCOVERY_MODE", "scheduled")
	request := appendStringField(nil, 1, "tenants/current")
	request = appendVarintField(request, 2, 2)
	request = appendVarintField(request, 4, 1)
	request = appendBytesField(request, 6, []byte{3, 4})
	response, err := newCompetitionServer().SearchCompetitions(violetAuthenticatedContext("u-owner"), &rawMsg{b: request})
	if err != nil {
		t.Fatal(err)
	}
	remaining := response.b
	count := 0
	for len(remaining) > 0 {
		number, typ, n := protowire.ConsumeTag(remaining)
		if n < 0 || number != 1 || typ != protowire.BytesType {
			t.Fatalf("unexpected scheduled search field %d type %d", number, typ)
		}
		competition, consumed := protowire.ConsumeBytes(remaining[n:])
		if consumed < 0 {
			t.Fatal(protowire.ParseError(consumed))
		}
		if len(competition) == 0 {
			t.Fatal("scheduled search returned an empty competition")
		}
		count++
		remaining = remaining[n+consumed:]
	}
	if count != 2 {
		t.Fatalf("scheduled search returned %d competitions, want 2", count)
	}
}

func TestRankedCompetitionDefaultDiscoveryUsesSafeScheduledProfile(t *testing.T) {
	t.Setenv("VIOLET_RANKED_DISCOVERY_MODE", "")
	request := appendStringField(nil, 1, "tenants/current")
	request = appendVarintField(request, 2, 2)
	request = appendVarintField(request, 4, 1)
	request = appendBytesField(request, 6, []byte{3})
	response, err := newCompetitionServer().SearchCompetitions(violetAuthenticatedContext("u-owner"), &rawMsg{b: request})
	if err != nil {
		t.Fatal(err)
	}
	_, outerType, outerTagSize := protowire.ConsumeTag(response.b)
	if outerTagSize < 0 || outerType != protowire.BytesType {
		t.Fatal("default ranked search did not return a competition resource")
	}
	competition, consumed := protowire.ConsumeBytes(response.b[outerTagSize:])
	if consumed < 0 {
		t.Fatal(protowire.ParseError(consumed))
	}
	want := map[protowire.Number]bool{1: false, 5: false, 26: false, 27: false, 28: false, 29: false, 33: false}
	for len(competition) > 0 {
		number, typ, n := protowire.ConsumeTag(competition)
		if n < 0 {
			t.Fatal(protowire.ParseError(n))
		}
		competition = competition[n:]
		fieldSize := protowire.ConsumeFieldValue(number, typ, competition)
		if fieldSize < 0 {
			t.Fatal(protowire.ParseError(fieldSize))
		}
		if number == 17 || number == 24 {
			t.Fatalf("unsafe ranked competition field %d is present by default", number)
		}
		if _, ok := want[number]; !ok {
			t.Fatalf("unexpected default ranked competition field %d", number)
		}
		want[number] = true
		competition = competition[fieldSize:]
	}
	for number, seen := range want {
		if !seen {
			t.Errorf("safe default ranked competition field %d is absent", number)
		}
	}
}

func TestRankedCompetitionIncludesOperationalFields(t *testing.T) {
	wire := marshalRankedCompetition(3, time.Unix(1_800_000_000, 0).UTC())
	want := map[protowire.Number]bool{1: false, 2: false, 5: false, 6: false, 7: false, 8: false, 9: false, 10: false, 14: false, 15: false, 16: false, 17: false, 18: false, 19: false, 20: false, 21: false, 24: false, 25: false, 26: false, 27: false, 28: false, 29: false, 30: false, 31: false, 33: false, 34: false, 35: false, 36: false}
	for len(wire) > 0 {
		number, typ, n := protowire.ConsumeTag(wire)
		if n < 0 {
			t.Fatal(protowire.ParseError(n))
		}
		wire = wire[n:]
		consumed := protowire.ConsumeFieldValue(number, typ, wire)
		if consumed < 0 {
			t.Fatal(protowire.ParseError(consumed))
		}
		if _, ok := want[number]; ok {
			want[number] = true
		}
		wire = wire[consumed:]
	}
	for number, seen := range want {
		if !seen {
			t.Errorf("ranked competition field %d is absent", number)
		}
	}
}

func TestCompetitionUserNameValidation(t *testing.T) {
	if err := validateCompetitionUserName("tenants/current/competitionUsers/current"); err != nil {
		t.Fatal(err)
	}
	if err := validateCompetitionUserName("tenants/current/users/current"); err == nil {
		t.Fatal("accepted a non-competition resource")
	}
}

func TestCompetitionParticipantWireContract(t *testing.T) {
	const name = "tenants/current/competitions/local-ranked-singles-1/participants/u-local"
	wire := marshalCompetitionParticipant(name, time.Unix(1_800_000_000, 0).UTC())
	want := map[protowire.Number]bool{1: false, 2: false, 3: false, 4: false, 6: false, 7: false, 8: false, 13: false, 14: false, 15: false, 16: false, 17: false, 18: false, 19: false, 20: false, 21: false, 22: false, 23: false, 24: false, 25: false}
	for len(wire) > 0 {
		number, typ, n := protowire.ConsumeTag(wire)
		if n < 0 {
			t.Fatal(protowire.ParseError(n))
		}
		wire = wire[n:]
		consumed := protowire.ConsumeFieldValue(number, typ, wire)
		if consumed < 0 {
			t.Fatal(protowire.ParseError(consumed))
		}
		if _, ok := want[number]; ok {
			want[number] = true
		} else {
			t.Fatalf("unexpected participant field %d", number)
		}
		wire = wire[consumed:]
	}
	for number, seen := range want {
		if !seen {
			t.Errorf("participant field %d is absent", number)
		}
	}
}

func TestCompetitionParticipantNameValidation(t *testing.T) {
	if err := validateCompetitionParticipantName("tenants/current/competitions/local-ranked-singles-1/participants/u-local"); err != nil {
		t.Fatal(err)
	}
	if err := validateCompetitionParticipantName("tenants/current/competitions/other/participants/u-local"); err == nil {
		t.Fatal("accepted a participant from an unknown competition")
	}
}

func TestRankedCompetitionNameValidation(t *testing.T) {
	competitionType, err := rankedCompetitionTypeFromName("tenants/current/competitions/local-ranked-singles-1")
	if err != nil || competitionType != 3 {
		t.Fatalf("singles type=%d error=%v", competitionType, err)
	}
	competitionType, err = rankedCompetitionTypeFromName("tenants/" + nplnTenantID + "/competitions/local-ranked-doubles-1")
	if err != nil || competitionType != 4 {
		t.Fatalf("doubles type=%d error=%v", competitionType, err)
	}
	if _, err := rankedCompetitionTypeFromName("tenants/current/competitions/other"); err == nil {
		t.Fatal("accepted an unknown ranked competition")
	}
}

func TestRankedCompetitionMinimalWireContract(t *testing.T) {
	wire := marshalRankedCompetitionMinimal("tenants/current/competitions/local-ranked-singles-1", 3)
	want := map[protowire.Number]bool{1: false, 5: false, 33: false}
	for len(wire) > 0 {
		number, typ, n := protowire.ConsumeTag(wire)
		if n < 0 {
			t.Fatal(protowire.ParseError(n))
		}
		wire = wire[n:]
		consumed := protowire.ConsumeFieldValue(number, typ, wire)
		if consumed < 0 {
			t.Fatal(protowire.ParseError(consumed))
		}
		if _, ok := want[number]; !ok {
			t.Fatalf("unexpected minimal competition field %d", number)
		}
		want[number] = true
		wire = wire[consumed:]
	}
	for number, seen := range want {
		if !seen {
			t.Errorf("minimal competition field %d is absent", number)
		}
	}
}

func TestRankedCompetitionScheduledWireContract(t *testing.T) {
	wire := marshalRankedCompetitionScheduled("tenants/current/competitions/local-ranked-singles-1", 3, time.Unix(1_800_000_000, 0).UTC())
	want := map[protowire.Number]bool{1: false, 5: false, 26: false, 27: false, 28: false, 29: false, 33: false}
	for len(wire) > 0 {
		number, typ, n := protowire.ConsumeTag(wire)
		if n < 0 {
			t.Fatal(protowire.ParseError(n))
		}
		wire = wire[n:]
		consumed := protowire.ConsumeFieldValue(number, typ, wire)
		if consumed < 0 {
			t.Fatal(protowire.ParseError(consumed))
		}
		if _, ok := want[number]; !ok {
			t.Fatalf("unexpected scheduled competition field %d", number)
		}
		want[number] = true
		wire = wire[consumed:]
	}
	for number, seen := range want {
		if !seen {
			t.Errorf("scheduled competition field %d is absent", number)
		}
	}
}
