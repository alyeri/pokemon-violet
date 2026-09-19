package main

import (
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
)

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
