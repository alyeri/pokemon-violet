package main

// competition implements the first Timber contract used by Pokemon
// Scarlet/Violet's Battle Stadium. Timber is title-specific, so this service
// uses the same carefully bounded protobuf-wire approach as trade_box.go.

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
)

const competitionServiceName = "nn.npln.timber.v1.CompetitionService"

type rankedNotification struct {
	name        string
	matchingKey string
	etag        string
	updateTime  time.Time
}

type rankedNotificationWaiter struct {
	uid  string
	wake chan struct{}
	ctx  context.Context
}

type competitionServer struct {
	mu            sync.Mutex
	waiters       map[string][]*rankedNotificationWaiter
	notifications map[string]rankedNotification
	users         map[string]competitionUserProfile
	hosted        map[string]hostedCompetition
	aliases       map[string]string
	official      map[string][]byte
	ranked        *rankedStore
}

type competitionUserProfile struct {
	birthmonth      string
	area            uint64
	language        string
	category        uint64
	currentOfficial string
	currentFriendly string
	initialized     bool
}

type hostedCompetition struct {
	owner       string
	competition []byte
	participant []byte
}

func newCompetitionServer(stores ...*rankedStore) *competitionServer {
	server := &competitionServer{
		waiters:       make(map[string][]*rankedNotificationWaiter),
		notifications: make(map[string]rankedNotification),
		users:         make(map[string]competitionUserProfile),
		hosted:        make(map[string]hostedCompetition),
		aliases:       map[string]string{"OFC001": officialCompetitionName()},
		official:      make(map[string][]byte),
	}
	if len(stores) > 0 {
		server.ranked = stores[0]
	}
	return server
}

func validateCompetitionUserName(name string) error {
	parts := strings.Split(name, "/")
	if len(parts) != 4 || parts[0] != "tenants" || parts[2] != "competitionUsers" ||
		(parts[1] != "current" && parts[1] != nplnTenantID) || parts[3] != "current" {
		return status.Error(codes.InvalidArgument, "invalid Violet competition-user resource name")
	}
	return nil
}

func rankedCompetitionName(competitionType uint64) string {
	kind := "singles"
	if competitionType == 4 {
		kind = "doubles"
	}
	return nplnTenant + "/competitions/local-ranked-" + kind + "-1"
}

func officialCompetitionName() string {
	return nplnTenant + "/competitions/local-online-official-1"
}

func canonicalOfficialCompetitionName(name string) (string, error) {
	parts := strings.Split(name, "/")
	if len(parts) != 4 || parts[0] != "tenants" ||
		(parts[1] != "current" && parts[1] != nplnTenantID) ||
		parts[2] != "competitions" || parts[3] != "local-online-official-1" {
		return "", status.Error(codes.InvalidArgument, "invalid official competition resource name")
	}
	return officialCompetitionName(), nil
}

func rankedCompetitionTypeFromName(name string) (uint64, error) {
	parts := strings.Split(name, "/")
	if len(parts) != 4 || parts[0] != "tenants" ||
		(parts[1] != "current" && parts[1] != nplnTenantID) || parts[2] != "competitions" {
		return 0, status.Error(codes.InvalidArgument, "invalid Violet ranked competition resource name")
	}
	switch parts[3] {
	case "local-ranked-singles-1":
		return 3, nil
	case "local-ranked-doubles-1":
		return 4, nil
	default:
		return 0, status.Error(codes.InvalidArgument, "unknown Violet ranked competition")
	}
}

func appendEmptyMessageField(out []byte, number protowire.Number) []byte {
	out = protowire.AppendTag(out, number, protowire.BytesType)
	return protowire.AppendBytes(out, nil)
}

func marshalCompetitionUser(name string, battleStats ...int32) []byte {
	return marshalCompetitionUserWithProfile(name, competitionUserProfile{language: "en", category: 3}, battleStats...)
}

func marshalCompetitionUserWithProfile(name string, profile competitionUserProfile, battleStats ...int32) []byte {
	// nn.npln.timber.v1.CompetitionUser:
	//   1 name, 4 language_code, 13 battle_stats, 14 category,
	//   15 current_ranked_singles_competition,
	//   17 current_ranked_doubles_competition, 19 create_time.
	//
	// Ranked discovery returns two active local seasons, so the current-user
	// resource must advertise the same resources. Leaving those references
	// absent while SearchCompetitions returned active seasons produced an
	// inconsistent state in Violet's ranked-screen callback.
	out := appendStringField(nil, 1, name)
	if profile.birthmonth != "" {
		out = appendStringField(out, 2, profile.birthmonth)
	}
	if profile.area != 0 {
		out = appendVarintField(out, 3, profile.area)
	}
	language := profile.language
	if language == "" {
		language = "en"
	}
	out = appendStringField(out, 4, language)
	if profile.currentFriendly != "" {
		out = appendStringField(out, 7, profile.currentFriendly)
	}
	if profile.currentOfficial != "" {
		out = appendStringField(out, 5, profile.currentOfficial)
	}
	stats := []byte(nil)
	if len(battleStats) > 0 && battleStats[0] != 0 {
		stats = appendVarintField(stats, 1, uint64(battleStats[0]))
	}
	if len(battleStats) > 1 && battleStats[1] != 0 {
		stats = appendVarintField(stats, 2, uint64(battleStats[1]))
	}
	if len(stats) == 0 {
		out = appendEmptyMessageField(out, 13)
	} else {
		out = appendBytesField(out, 13, stats)
	}
	category := profile.category
	if category == 0 {
		category = 3
	}
	out = appendVarintField(out, 14, category)
	out = appendStringField(out, 15, rankedCompetitionName(3))
	out = appendStringField(out, 17, rankedCompetitionName(4))
	return appendBytesField(out, 19, marshalTimestamp(time.Now().UTC().Add(-24*time.Hour)))
}

func (s *competitionServer) GetCompetitionUser(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	uid, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	name, err := parseProtoStringMessage(message.b)
	if err != nil {
		return nil, err
	}
	if err := validateCompetitionUserName(name); err != nil {
		return nil, err
	}
	battles, disconnects := int32(0), int32(0)
	if s.ranked != nil {
		battles, disconnects = s.ranked.battleStats(uid)
	}
	s.mu.Lock()
	profile := s.users[uid]
	s.mu.Unlock()
	log.Printf("[NPLN Timber] GetCompetitionUser name=%q battles=%d disconnects=%d", name, battles, disconnects)
	return &rawMsg{b: marshalCompetitionUserWithProfile(name, profile, battles, disconnects)}, nil
}

type updateCompetitionUserRequest struct {
	name       string
	birthmonth string
	area       uint64
	language   string
	category   uint64
	mask       []string
}

func parseCompetitionUserUpdate(data []byte) (updateCompetitionUserRequest, error) {
	var request updateCompetitionUserRequest
	for len(data) > 0 {
		number, typ, tagSize := protowire.ConsumeTag(data)
		if tagSize < 0 {
			return request, status.Error(codes.InvalidArgument, "malformed UpdateCompetitionUser request")
		}
		data = data[tagSize:]
		if typ != protowire.BytesType || (number != 1 && number != 2) {
			fieldSize := protowire.ConsumeFieldValue(number, typ, data)
			if fieldSize < 0 {
				return request, status.Error(codes.InvalidArgument, "malformed UpdateCompetitionUser field")
			}
			data = data[fieldSize:]
			continue
		}
		value, valueSize := protowire.ConsumeBytes(data)
		if valueSize < 0 {
			return request, status.Error(codes.InvalidArgument, "malformed UpdateCompetitionUser message")
		}
		if number == 1 {
			for len(value) > 0 {
				field, fieldType, fieldTagSize := protowire.ConsumeTag(value)
				if fieldTagSize < 0 {
					return request, status.Error(codes.InvalidArgument, "malformed CompetitionUser")
				}
				value = value[fieldTagSize:]
				var consumed int
				switch {
				case (field == 1 || field == 2 || field == 4) && fieldType == protowire.BytesType:
					text, n := protowire.ConsumeString(value)
					if n < 0 {
						return request, status.Error(codes.InvalidArgument, "malformed CompetitionUser string")
					}
					if field == 1 {
						request.name = text
					} else if field == 2 {
						request.birthmonth = text
					} else {
						request.language = text
					}
					consumed = n
				case (field == 3 || field == 14) && fieldType == protowire.VarintType:
					numberValue, n := protowire.ConsumeVarint(value)
					if n < 0 {
						return request, status.Error(codes.InvalidArgument, "malformed CompetitionUser integer")
					}
					if field == 3 {
						request.area = numberValue
					} else {
						request.category = numberValue
					}
					consumed = n
				default:
					consumed = protowire.ConsumeFieldValue(field, fieldType, value)
				}
				if consumed < 0 {
					return request, status.Error(codes.InvalidArgument, "malformed CompetitionUser field")
				}
				value = value[consumed:]
			}
		} else {
			for len(value) > 0 {
				field, fieldType, fieldTagSize := protowire.ConsumeTag(value)
				if fieldTagSize < 0 {
					return request, status.Error(codes.InvalidArgument, "malformed update mask")
				}
				value = value[fieldTagSize:]
				if field != 1 || fieldType != protowire.BytesType {
					return request, status.Error(codes.InvalidArgument, "unsupported update mask field")
				}
				path, n := protowire.ConsumeString(value)
				if n < 0 {
					return request, status.Error(codes.InvalidArgument, "malformed update mask path")
				}
				request.mask = append(request.mask, path)
				value = value[n:]
			}
		}
		data = data[valueSize:]
	}
	return request, nil
}

func (s *competitionServer) UpdateCompetitionUser(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	uid, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	request, err := parseCompetitionUserUpdate(message.b)
	if err != nil {
		return nil, err
	}
	if err := validateCompetitionUserName(request.name); err != nil {
		return nil, err
	}
	if len(request.mask) == 0 {
		return nil, status.Error(codes.InvalidArgument, "competition-user update mask is required")
	}
	allowed := map[string]bool{"birthmonth": true, "area": true, "language_code": true, "category": true}
	for _, path := range request.mask {
		if !allowed[path] {
			return nil, status.Errorf(codes.InvalidArgument, "unsupported competition-user update path %q", path)
		}
	}
	if request.area > uint64(^uint32(0)>>1) || request.category < 1 || request.category > 3 || request.language == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid Violet competition-user profile")
	}
	profile := competitionUserProfile{
		birthmonth:  request.birthmonth,
		area:        request.area,
		language:    request.language,
		category:    request.category,
		initialized: true,
	}
	s.mu.Lock()
	profile.currentOfficial = s.users[uid].currentOfficial
	profile.currentFriendly = s.users[uid].currentFriendly
	s.users[uid] = profile
	s.mu.Unlock()
	battles, disconnects := int32(0), int32(0)
	if s.ranked != nil {
		battles, disconnects = s.ranked.battleStats(uid)
	}
	log.Printf("[NPLN Timber] UpdateCompetitionUser uid=%q area=%d language=%q category=%d mask=%v", uid, profile.area, profile.language, profile.category, request.mask)
	return &rawMsg{b: marshalCompetitionUserWithProfile(request.name, profile, battles, disconnects)}, nil
}

type hostCompetitionRequest struct {
	tenant      string
	competition []byte
	participant []byte
}

func parseHostCompetitionRequest(data []byte) (hostCompetitionRequest, error) {
	var request hostCompetitionRequest
	for len(data) > 0 {
		number, typ, tagSize := protowire.ConsumeTag(data)
		if tagSize < 0 || typ != protowire.BytesType {
			return request, status.Error(codes.InvalidArgument, "malformed HostCompetition request")
		}
		data = data[tagSize:]
		value, valueSize := protowire.ConsumeBytes(data)
		if valueSize < 0 {
			return request, status.Error(codes.InvalidArgument, "malformed HostCompetition message")
		}
		switch number {
		case 1:
			request.tenant = string(value)
		case 2:
			request.competition = append([]byte(nil), value...)
		case 3:
			request.participant = append([]byte(nil), value...)
		}
		data = data[valueSize:]
	}
	if request.competition == nil || request.participant == nil {
		return request, status.Error(codes.InvalidArgument, "competition and participant are required")
	}
	return request, nil
}

func competitionIdentity(data []byte) (string, uint64, error) {
	var name string
	var competitionType uint64
	for len(data) > 0 {
		number, typ, tagSize := protowire.ConsumeTag(data)
		if tagSize < 0 {
			return "", 0, status.Error(codes.InvalidArgument, "malformed Competition")
		}
		data = data[tagSize:]
		var consumed int
		if number == 1 && typ == protowire.BytesType {
			name, consumed = protowire.ConsumeString(data)
		} else if number == 33 && typ == protowire.VarintType {
			competitionType, consumed = protowire.ConsumeVarint(data)
		} else {
			consumed = protowire.ConsumeFieldValue(number, typ, data)
		}
		if consumed < 0 {
			return "", 0, status.Error(codes.InvalidArgument, "malformed Competition field")
		}
		data = data[consumed:]
	}
	return name, competitionType, nil
}

func replaceProtoStringField(data []byte, target protowire.Number, replacement string) ([]byte, error) {
	out := make([]byte, 0, len(data)+len(replacement))
	replaced := false
	for len(data) > 0 {
		original := data
		number, typ, tagSize := protowire.ConsumeTag(data)
		if tagSize < 0 {
			return nil, status.Error(codes.InvalidArgument, "malformed protobuf resource")
		}
		data = data[tagSize:]
		valueSize := protowire.ConsumeFieldValue(number, typ, data)
		if valueSize < 0 {
			return nil, status.Error(codes.InvalidArgument, "malformed protobuf field")
		}
		if number == target {
			if typ != protowire.BytesType {
				return nil, status.Error(codes.InvalidArgument, "resource name has invalid wire type")
			}
			if !replaced {
				out = appendStringField(out, target, replacement)
				replaced = true
			}
		} else {
			out = append(out, original[:tagSize+valueSize]...)
		}
		data = data[valueSize:]
	}
	if !replaced {
		out = appendStringField(out, target, replacement)
	}
	return out, nil
}

func replaceProtoEncodedField(data []byte, target protowire.Number, expectedType protowire.Type, replacement []byte) ([]byte, error) {
	out := make([]byte, 0, len(data)+len(replacement))
	replaced := false
	for len(data) > 0 {
		original := data
		number, typ, tagSize := protowire.ConsumeTag(data)
		if tagSize < 0 {
			return nil, status.Error(codes.InvalidArgument, "malformed protobuf resource")
		}
		data = data[tagSize:]
		valueSize := protowire.ConsumeFieldValue(number, typ, data)
		if valueSize < 0 {
			return nil, status.Error(codes.InvalidArgument, "malformed protobuf field")
		}
		if number == target {
			if typ != expectedType {
				return nil, status.Error(codes.InvalidArgument, "protobuf field has invalid wire type")
			}
			if !replaced {
				out = append(out, replacement...)
				replaced = true
			}
		} else {
			out = append(out, original[:tagSize+valueSize]...)
		}
		data = data[valueSize:]
	}
	if !replaced {
		out = append(out, replacement...)
	}
	return out, nil
}

func canonicalHostedCompetitionName(name string) (string, error) {
	parts := strings.Split(name, "/")
	if len(parts) != 4 || parts[0] != "tenants" || parts[2] != "competitions" ||
		(parts[1] != "current" && parts[1] != nplnTenantID) {
		return "", status.Error(codes.InvalidArgument, "invalid hosted competition resource name")
	}
	return nplnTenant + "/competitions/" + parts[3], nil
}

const competitionIDAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

func friendlyCompetitionID(resource string) string {
	var hash uint64 = 14695981039346656037
	for _, value := range []byte(resource) {
		hash ^= uint64(value)
		hash *= 1099511628211
	}
	code := make([]byte, 6)
	for index := range code {
		code[index] = competitionIDAlphabet[hash%uint64(len(competitionIDAlphabet))]
		hash /= uint64(len(competitionIDAlphabet))
	}
	return string(code)
}

func competitionAliasName(code string) string {
	return nplnTenant + "/competitionAliases/" + code
}

func parseCompetitionAliasName(name string) (string, error) {
	parts := strings.Split(name, "/")
	if len(parts) != 4 || parts[0] != "tenants" ||
		(parts[1] != "current" && parts[1] != nplnTenantID) ||
		parts[2] != "competitionAliases" {
		return "", status.Error(codes.InvalidArgument, "invalid competition alias resource name")
	}
	code := strings.ToUpper(parts[3])
	if len(code) != 6 {
		return "", status.Error(codes.InvalidArgument, "competition ID must contain six characters")
	}
	for _, character := range code {
		if !strings.ContainsRune(competitionIDAlphabet, character) {
			return "", status.Error(codes.InvalidArgument, "competition ID must be alphanumeric")
		}
	}
	return code, nil
}

func (s *competitionServer) HostCompetition(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	uid, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	request, err := parseHostCompetitionRequest(message.b)
	if err != nil {
		return nil, err
	}
	if request.tenant != "tenants/current" && request.tenant != nplnTenant {
		return nil, status.Error(codes.InvalidArgument, "invalid hosted competition tenant")
	}
	requestedName, competitionType, err := competitionIdentity(request.competition)
	if err != nil {
		return nil, err
	}
	if competitionType != 2 {
		return nil, status.Error(codes.InvalidArgument, "HostCompetition requires a friendly competition")
	}
	name, err := canonicalHostedCompetitionName(requestedName)
	if err != nil {
		return nil, err
	}
	// Violet submits the collection resource (`.../competitions/`) and expects
	// HostCompetition to allocate the final competition resource identifier.
	if strings.HasSuffix(name, "/") {
		id, idErr := newResourceUUID()
		if idErr != nil {
			return nil, status.Error(codes.Internal, "failed to allocate competition resource")
		}
		name += id
	}
	competitionID := friendlyCompetitionID(name)
	aliasName := competitionAliasName(competitionID)
	competition, err := replaceProtoStringField(request.competition, 1, name)
	if err != nil {
		return nil, err
	}
	competition, err = replaceProtoStringField(competition, 2, aliasName)
	if err != nil {
		return nil, err
	}
	participantName := name + "/participants/" + uid
	participant, err := replaceProtoStringField(request.participant, 1, participantName)
	if err != nil {
		return nil, err
	}
	participant = appendVarintField(participant, 6, 1) // ACCEPTED.
	participant = appendBytesField(participant, 7, marshalTimestamp(time.Now().UTC()))

	s.mu.Lock()
	if existing, exists := s.hosted[name]; exists && existing.owner != uid {
		s.mu.Unlock()
		return nil, status.Error(codes.AlreadyExists, "competition resource is already hosted")
	}
	s.hosted[name] = hostedCompetition{owner: uid, competition: competition, participant: participant}
	s.aliases[competitionID] = name
	profile := s.users[uid]
	profile.currentFriendly = name
	s.users[uid] = profile
	s.mu.Unlock()

	log.Printf("[NPLN Timber] HostCompetition uid=%q name=%q id=%q type=FRIENDLY competition_bytes=%d participant_bytes=%d", uid, name, competitionID, len(competition), len(participant))
	response := appendBytesField(nil, 1, competition)
	response = appendBytesField(response, 2, participant)
	return &rawMsg{b: response}, nil
}

func (s *competitionServer) GetCompetitionAlias(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	if _, err := authenticatedNPLNUID(ctx); err != nil {
		return nil, err
	}
	name, err := parseProtoStringMessage(message.b)
	if err != nil {
		return nil, err
	}
	code, err := parseCompetitionAliasName(name)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	competition := s.aliases[code]
	s.mu.Unlock()
	if competition == "" {
		return nil, status.Error(codes.NotFound, "competition ID not found")
	}
	canonicalName := competitionAliasName(code)
	response := appendStringField(nil, 1, canonicalName)
	response = appendStringField(response, 2, competition)
	log.Printf("[NPLN Timber] GetCompetitionAlias id=%q competition=%q", code, competition)
	return &rawMsg{b: response}, nil
}

type createCompetitionParticipantRequest struct {
	parent      string
	participant []byte
}

func parseCreateCompetitionParticipantRequest(data []byte) (createCompetitionParticipantRequest, error) {
	var request createCompetitionParticipantRequest
	for len(data) > 0 {
		number, typ, tagSize := protowire.ConsumeTag(data)
		if tagSize < 0 {
			return request, status.Error(codes.InvalidArgument, "malformed CreateCompetitionParticipant request")
		}
		data = data[tagSize:]
		if typ != protowire.BytesType || (number != 1 && number != 2) {
			fieldSize := protowire.ConsumeFieldValue(number, typ, data)
			if fieldSize < 0 {
				return request, status.Error(codes.InvalidArgument, "malformed CreateCompetitionParticipant field")
			}
			data = data[fieldSize:]
			continue
		}
		value, valueSize := protowire.ConsumeBytes(data)
		if valueSize < 0 {
			return request, status.Error(codes.InvalidArgument, "malformed CreateCompetitionParticipant value")
		}
		if number == 1 {
			request.parent = string(value)
		} else {
			request.participant = append([]byte(nil), value...)
		}
		data = data[valueSize:]
	}
	if request.parent == "" || len(request.participant) == 0 {
		return request, status.Error(codes.InvalidArgument, "parent and competition participant are required")
	}
	return request, nil
}

func validateOfficialCompetitionParent(parent string) error {
	parts := strings.Split(parent, "/")
	if len(parts) != 4 || parts[0] != "tenants" ||
		(parts[1] != "current" && parts[1] != nplnTenantID) ||
		parts[2] != "competitions" || parts[3] != "local-online-official-1" {
		return status.Error(codes.InvalidArgument, "invalid official competition parent")
	}
	return nil
}

func validateRegistrationParticipant(data []byte) (int, error) {
	var deviceID, utcOffset string
	binarySize := 0
	for len(data) > 0 {
		number, typ, tagSize := protowire.ConsumeTag(data)
		if tagSize < 0 {
			return 0, status.Error(codes.InvalidArgument, "malformed competition participant")
		}
		data = data[tagSize:]
		fieldSize := protowire.ConsumeFieldValue(number, typ, data)
		if fieldSize < 0 {
			return 0, status.Error(codes.InvalidArgument, "malformed competition participant field")
		}
		if typ == protowire.BytesType && (number == 3 || number == 4 || number == 5) {
			value, consumed := protowire.ConsumeBytes(data)
			if consumed < 0 {
				return 0, status.Error(codes.InvalidArgument, "malformed competition participant data")
			}
			switch number {
			case 3:
				deviceID = string(value)
			case 4:
				utcOffset = string(value)
			case 5:
				binarySize = len(value)
			}
		}
		data = data[fieldSize:]
	}
	if deviceID == "" || utcOffset == "" || binarySize == 0 || binarySize > 64*1024 {
		return 0, status.Error(codes.InvalidArgument, "incomplete competition participant registration")
	}
	return binarySize, nil
}

func (s *competitionServer) CreateCompetitionParticipant(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	uid, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	request, err := parseCreateCompetitionParticipantRequest(message.b)
	if err != nil {
		return nil, err
	}
	if err := validateOfficialCompetitionParent(request.parent); err != nil {
		return nil, err
	}
	binarySize, err := validateRegistrationParticipant(request.participant)
	if err != nil {
		return nil, err
	}
	name := officialCompetitionName() + "/participants/" + uid
	participant, err := replaceProtoStringField(request.participant, 1, name)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	participant = appendVarintField(participant, 6, 1) // ACCEPTED until activation.
	participant = appendBytesField(participant, 7, marshalTimestamp(now))
	// Official competitions use the same CompetitionParticipant rating field as
	// Ranked. Leaving it absent made Violet display 0.000, create a rate=0
	// matchmaking ticket, and reject the otherwise healthy two-player session
	// after re-reading its active participant. A new participant starts at 1500;
	// rank remains absent until the competition has standings.
	participant = appendVarintField(participant, 13, rankedInitialRating)

	s.mu.Lock()
	profile := s.users[uid]
	participant = appendVarintField(participant, 25, profile.category)
	participant = appendVarintField(participant, 26, profile.area)
	s.official[uid] = append([]byte(nil), participant...)
	profile.currentOfficial = officialCompetitionName()
	s.users[uid] = profile
	s.mu.Unlock()

	log.Printf("[NPLN Timber] CreateCompetitionParticipant uid=%q competition=%q state=ACCEPTED rating=%d participant_binary=%d", uid, officialCompetitionName(), rankedInitialRating, binarySize)
	return &rawMsg{b: participant}, nil
}

type activateCompetitionParticipantRequest struct {
	name              string
	participantBinary []byte
}

func parseActivateCompetitionParticipantRequest(data []byte) (activateCompetitionParticipantRequest, error) {
	var request activateCompetitionParticipantRequest
	for len(data) > 0 {
		number, typ, tagSize := protowire.ConsumeTag(data)
		if tagSize < 0 {
			return request, status.Error(codes.InvalidArgument, "malformed ActivateCompetitionParticipant request")
		}
		data = data[tagSize:]
		valueSize := protowire.ConsumeFieldValue(number, typ, data)
		if valueSize < 0 {
			return request, status.Error(codes.InvalidArgument, "malformed ActivateCompetitionParticipant field")
		}
		if number == 1 && typ == protowire.BytesType {
			value, consumed := protowire.ConsumeString(data)
			if consumed < 0 {
				return request, status.Error(codes.InvalidArgument, "malformed participant name")
			}
			request.name = value
		} else if number == 2 && typ == protowire.BytesType {
			wrapper, consumed := protowire.ConsumeBytes(data)
			if consumed < 0 {
				return request, status.Error(codes.InvalidArgument, "malformed participant binary wrapper")
			}
			for len(wrapper) > 0 {
				wrappedNumber, wrappedType, wrappedTagSize := protowire.ConsumeTag(wrapper)
				if wrappedTagSize < 0 {
					return request, status.Error(codes.InvalidArgument, "malformed participant binary")
				}
				wrapper = wrapper[wrappedTagSize:]
				wrappedSize := protowire.ConsumeFieldValue(wrappedNumber, wrappedType, wrapper)
				if wrappedSize < 0 {
					return request, status.Error(codes.InvalidArgument, "malformed participant binary value")
				}
				if wrappedNumber == 1 && wrappedType == protowire.BytesType {
					value, valueSize := protowire.ConsumeBytes(wrapper)
					if valueSize < 0 || len(value) > 64*1024 {
						return request, status.Error(codes.InvalidArgument, "invalid participant binary")
					}
					request.participantBinary = append([]byte(nil), value...)
				}
				wrapper = wrapper[wrappedSize:]
			}
		}
		data = data[valueSize:]
	}
	if request.name == "" {
		return request, status.Error(codes.InvalidArgument, "participant name is required")
	}
	return request, nil
}

func (s *competitionServer) ActivateCompetitionParticipant(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	uid, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	request, err := parseActivateCompetitionParticipantRequest(message.b)
	if err != nil {
		return nil, err
	}
	competitionName, _, err := hostedParticipantNames(request.name)
	if err != nil || competitionName != officialCompetitionName() {
		return nil, status.Error(codes.InvalidArgument, "invalid official competition participant")
	}
	requestedParticipant := strings.Split(request.name, "/")[5]
	if requestedParticipant != "current" && requestedParticipant != uid {
		return nil, status.Error(codes.PermissionDenied, "competition participant does not match caller")
	}

	s.mu.Lock()
	participant, ok := s.official[uid]
	if !ok {
		s.mu.Unlock()
		return nil, status.Error(codes.NotFound, "official competition participant not found")
	}
	if len(request.participantBinary) > 0 {
		participant, err = replaceProtoEncodedField(participant, 5, protowire.BytesType, appendBytesField(nil, 5, request.participantBinary))
	}
	if err == nil {
		participant, err = replaceProtoEncodedField(participant, 6, protowire.VarintType, appendVarintField(nil, 6, 2))
	}
	if err == nil {
		participant, err = replaceProtoEncodedField(participant, 8, protowire.BytesType, appendBytesField(nil, 8, marshalTimestamp(time.Now().UTC())))
	}
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.official[uid] = append([]byte(nil), participant...)
	s.mu.Unlock()

	log.Printf("[NPLN Timber] ActivateCompetitionParticipant uid=%q competition=%q state=ACTIVE binary_override=%d", uid, competitionName, len(request.participantBinary))
	return &rawMsg{b: participant}, nil
}

func validateCompetitionParticipantName(name string) error {
	parts := strings.Split(name, "/")
	if len(parts) != 6 || parts[0] != "tenants" ||
		(parts[1] != "current" && parts[1] != nplnTenantID) ||
		parts[2] != "competitions" || parts[4] != "participants" || parts[5] == "" ||
		(parts[3] != "local-ranked-singles-1" && parts[3] != "local-ranked-doubles-1") {
		return status.Error(codes.InvalidArgument, "invalid Violet competition-participant resource name")
	}
	return nil
}

func hostedParticipantNames(name string) (string, string, error) {
	parts := strings.Split(name, "/")
	if len(parts) != 6 || parts[0] != "tenants" ||
		(parts[1] != "current" && parts[1] != nplnTenantID) ||
		parts[2] != "competitions" || parts[3] == "" ||
		parts[4] != "participants" || parts[5] == "" {
		return "", "", status.Error(codes.InvalidArgument, "invalid hosted competition-participant resource name")
	}
	competition := nplnTenant + "/competitions/" + parts[3]
	participant := competition + "/participants/" + parts[5]
	return competition, participant, nil
}

func marshalCompetitionParticipant(name string, now time.Time, ranked ...rankedStats) []byte {
	// nn.npln.timber.v1.CompetitionParticipant. This is the local participant
	// referenced by CompetitionUser. Zero-valued counters remain absent under
	// proto3; Violet receives an active MASTER participant with a deterministic
	// initial rating and lifecycle timestamps.
	stats := defaultRankedStats()
	if len(ranked) > 0 {
		stats = ranked[0]
	}
	out := appendStringField(nil, 1, name)
	out = appendVarintField(out, 2, 1) // Local slot.
	out = appendStringField(out, 3, "nextendo-local")
	out = appendStringField(out, 4, "+00:00")
	out = appendVarintField(out, 6, 2) // ACTIVE.
	out = appendBytesField(out, 7, marshalTimestamp(now.Add(-24*time.Hour)))
	out = appendBytesField(out, 8, marshalTimestamp(now.Add(-24*time.Hour)))
	out = appendVarintField(out, 13, uint64(stats.Rating))
	out = appendVarintField(out, 14, uint64(stats.Rank))
	out = appendVarintField(out, 15, uint64(stats.MatchCount))
	out = appendVarintField(out, 16, uint64(stats.WinCount))
	out = appendVarintField(out, 17, uint64(stats.LoseCount))
	out = appendVarintField(out, 18, uint64(stats.DrawCount))
	out = appendVarintField(out, 19, uint64(stats.DisconnectCount))
	out = appendVarintField(out, 20, uint64(stats.NoContestCount))
	out = appendVarintField(out, 21, uint64(stats.WinStreakCount))
	out = appendVarintField(out, 22, uint64(stats.LoseStreakCount))
	out = appendVarintField(out, 23, uint64(stats.Point))
	out = appendVarintField(out, 24, uint64(stats.MaxPoint))
	out = appendVarintField(out, 25, 3) // MASTER.
	return out
}

func (s *competitionServer) GetCompetitionParticipant(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	uid, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	name, err := parseProtoStringMessage(message.b)
	if err != nil {
		return nil, err
	}
	competitionName, participantName, hostedNameErr := hostedParticipantNames(name)
	if hostedNameErr == nil {
		if competitionName == officialCompetitionName() {
			s.mu.Lock()
			participant, ok := s.official[uid]
			s.mu.Unlock()
			if !ok {
				return nil, status.Error(codes.NotFound, "official competition participant not found")
			}
			// Violet may address its participant through the title-owned alias
			// stored in the save rather than the authenticated NPLN UID. As with
			// Friendly participants and Ranked synthetic participants, the
			// returned resource must use the exact resolved name requested by the
			// client. Returning the UID-keyed storage name makes the Official
			// battle flow repeatedly fetch the participant and time out before its
			// final __us readiness subscription.
			participant, err = replaceProtoStringField(participant, 1, participantName)
			if err != nil {
				return nil, err
			}
			log.Printf("[NPLN Timber] GetCompetitionParticipant name=%q type=OFFICIAL", participantName)
			return &rawMsg{b: participant}, nil
		}
		s.mu.Lock()
		hosted, ok := s.hosted[competitionName]
		s.mu.Unlock()
		if ok {
			participant, replaceErr := replaceProtoStringField(hosted.participant, 1, participantName)
			if replaceErr != nil {
				return nil, replaceErr
			}
			log.Printf("[NPLN Timber] GetCompetitionParticipant name=%q type=FRIENDLY", participantName)
			return &rawMsg{b: participant}, nil
		}
	}
	if err := validateCompetitionParticipantName(name); err != nil {
		return nil, err
	}
	parts := strings.Split(name, "/")
	stats := s.ranked.stats(parts[3], uid)
	log.Printf("[NPLN Timber] GetCompetitionParticipant name=%q state=ACTIVE rating=%d rank=%d matches=%d wins=%d losses=%d", name, stats.Rating, stats.Rank, stats.MatchCount, stats.WinCount, stats.LoseCount)
	return &rawMsg{b: marshalCompetitionParticipant(name, time.Now().UTC(), stats)}, nil
}

func rankedNotificationCompetition(name string) (string, error) {
	parts := strings.Split(name, "/")
	if len(parts) != 7 || parts[4] != "participants" || parts[5] == "" || parts[6] != "notification" {
		return "", status.Error(codes.InvalidArgument, "invalid Violet competition notification resource name")
	}
	competition := strings.Join(parts[:4], "/")
	if official, officialErr := canonicalOfficialCompetitionName(competition); officialErr == nil {
		return official, nil
	}
	kind, err := rankedCompetitionTypeFromName(competition)
	if err != nil {
		return "", err
	}
	return rankedCompetitionName(kind), nil
}

func rankedNotificationKey(uid, competition string) string { return uid + "\x00" + competition }

func marshalCompetitionNotification(name, matchingKey, etag string, now time.Time) []byte {
	out := appendStringField(nil, 1, name)
	out = appendBytesField(out, 2, marshalTimestamp(now))
	out = appendStringField(out, 4, matchingKey)
	if etag != "" {
		out = appendStringField(out, 99, etag)
	}
	return out
}

func (s *competitionServer) GetCompetitionNotification(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	uid, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	name, err := parseProtoStringMessage(message.b)
	if err != nil {
		return nil, err
	}
	competition, err := rankedNotificationCompetition(name)
	if err != nil {
		return nil, err
	}
	if participant := strings.Split(name, "/")[5]; participant != "current" && participant != uid {
		return nil, status.Error(codes.PermissionDenied, "notification participant does not match caller")
	}
	s.mu.Lock()
	notification, ok := s.notifications[rankedNotificationKey(uid, competition)]
	s.mu.Unlock()
	if !ok {
		return nil, status.Error(codes.NotFound, "no competition notification is pending")
	}
	log.Printf("[NPLN Timber] GetCompetitionNotification uid=%q competition=%q matching_key=%q", uid, competition, notification.matchingKey)
	return &rawMsg{b: marshalCompetitionNotification(name, notification.matchingKey, notification.etag, notification.updateTime)}, nil
}

type searchCompetitionsRequest struct {
	tenant   string
	pageSize uint64
	view     uint64
	types    []uint64
}

func parseSearchCompetitionsRequest(data []byte) (searchCompetitionsRequest, error) {
	var request searchCompetitionsRequest
	for len(data) > 0 {
		number, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return request, protowire.ParseError(n)
		}
		data = data[n:]
		switch number {
		case 1:
			if typ != protowire.BytesType || request.tenant != "" {
				return request, status.Error(codes.InvalidArgument, "invalid ranked competition tenant")
			}
			value, consumed := protowire.ConsumeString(data)
			if consumed < 0 {
				return request, protowire.ParseError(consumed)
			}
			request.tenant, n = value, consumed
		case 2, 4:
			if typ != protowire.VarintType {
				return request, status.Error(codes.InvalidArgument, "invalid ranked competition scalar")
			}
			value, consumed := protowire.ConsumeVarint(data)
			if consumed < 0 {
				return request, protowire.ParseError(consumed)
			}
			if number == 2 {
				request.pageSize = value
			} else {
				request.view = value
			}
			n = consumed
		case 6:
			if typ != protowire.BytesType {
				return request, status.Error(codes.InvalidArgument, "ranked competition types must be packed")
			}
			packed, consumed := protowire.ConsumeBytes(data)
			if consumed < 0 {
				return request, protowire.ParseError(consumed)
			}
			for len(packed) > 0 {
				value, itemSize := protowire.ConsumeVarint(packed)
				if itemSize < 0 {
					return request, protowire.ParseError(itemSize)
				}
				request.types = append(request.types, value)
				packed = packed[itemSize:]
			}
			n = consumed
		default:
			consumed := protowire.ConsumeFieldValue(number, typ, data)
			if consumed < 0 {
				return request, protowire.ParseError(consumed)
			}
			n = consumed
		}
		data = data[n:]
	}
	if request.tenant != "tenants/current" && request.tenant != nplnTenant {
		return request, status.Error(codes.InvalidArgument, "invalid ranked competition tenant")
	}
	if request.pageSize < 1 || request.pageSize > 100 || request.view != 1 || len(request.types) == 0 || len(request.types) > 2 {
		return request, status.Error(codes.InvalidArgument, "unsupported competition search")
	}
	for _, competitionType := range request.types {
		if competitionType != 1 && competitionType != 3 && competitionType != 4 {
			return request, status.Error(codes.InvalidArgument, "unsupported competition type")
		}
	}
	return request, nil
}

func appendVarintField(out []byte, number protowire.Number, value uint64) []byte {
	out = protowire.AppendTag(out, number, protowire.VarintType)
	return protowire.AppendVarint(out, value)
}

func appendStringMapField(out []byte, number protowire.Number, key, value string) []byte {
	entry := appendStringField(nil, 1, key)
	entry = appendStringField(entry, 2, value)
	return appendBytesField(out, number, entry)
}

func marshalRankedCompetition(competitionType uint64, now time.Time) []byte {
	return marshalRankedCompetitionNamed(rankedCompetitionName(competitionType), competitionType, now)
}

func marshalRankedCompetitionMinimal(name string, competitionType uint64) []byte {
	// BASIC diagnostic floor: identity only. This intentionally omits every
	// optional season field so a client abort can be separated from malformed
	// or title-specific application_data in the synthetic full resource.
	out := appendStringField(nil, 1, name)
	out = appendVarintField(out, 5, 1)
	return appendVarintField(out, 33, competitionType)
}

func marshalRankedCompetitionScheduled(name string, competitionType uint64, now time.Time) []byte {
	out := marshalRankedCompetitionMinimal(name, competitionType)
	out = appendBytesField(out, 26, marshalTimestamp(now.Add(-24*time.Hour)))
	out = appendBytesField(out, 27, marshalTimestamp(now.Add(365*24*time.Hour)))
	out = appendBytesField(out, 28, marshalTimestamp(now.Add(-24*time.Hour)))
	out = appendBytesField(out, 29, marshalTimestamp(now.Add(365*24*time.Hour)))
	return out
}

func marshalRankedCompetitionNamed(name string, competitionType uint64, now time.Time) []byte {
	kind := "singles"
	subtitle := "Single Battle"
	if competitionType == 4 {
		kind = "doubles"
		subtitle = "Double Battle"
	}
	competitionID := "local-ranked-" + kind + "-1"
	out := appendStringField(nil, 1, name)
	out = appendStringField(out, 2, nplnTenant+"/competitionAliases/"+competitionID)
	out = appendVarintField(out, 33, competitionType)
	out = appendVarintField(out, 5, 1)
	out = appendStringMapField(out, 6, "en", "Ranked Battles")
	out = appendStringMapField(out, 7, "en", subtitle)
	out = appendVarintField(out, 8, 3) // ANYBODY may read the public season.
	out = appendVarintField(out, 9, 1) // Only the operator/owner may mutate it.
	out = appendVarintField(out, 10, 1)
	out = appendBytesField(out, 34, []byte{3}) // MASTER category.
	out = appendVarintField(out, 14, 1)
	out = appendVarintField(out, 15, 100000)
	out = appendVarintField(out, 16, 1)
	out = appendStringField(out, 17, "RankBattle")
	out = appendVarintField(out, 18, 1) // The server creates matching keys.
	hours := make([]byte, 0, 24)
	for hour := uint64(0); hour < 24; hour++ {
		hours = protowire.AppendVarint(hours, hour)
	}
	out = appendBytesField(out, 19, hours)
	out = appendVarintField(out, 20, 9999)
	out = appendVarintField(out, 21, 9999)
	out = appendVarintField(out, 35, 1)
	// application_data is a message field. Presence is observable in the C++
	// client even when the contained map is empty; ranked discovery expects a
	// Competition-owned application-data object before it reads title-specific
	// rules. Keep the map empty until its Scarlet/Violet schema is observed.
	out = appendEmptyMessageField(out, 24)
	out = appendBytesField(out, 25, marshalTimestamp(now.Add(-48*time.Hour)))
	out = appendBytesField(out, 36, marshalTimestamp(now.Add(-24*time.Hour)))
	out = appendBytesField(out, 26, marshalTimestamp(now.Add(-24*time.Hour)))
	out = appendBytesField(out, 27, marshalTimestamp(now.Add(365*24*time.Hour)))
	out = appendBytesField(out, 28, marshalTimestamp(now.Add(-24*time.Hour)))
	out = appendBytesField(out, 29, marshalTimestamp(now.Add(365*24*time.Hour)))
	out = appendBytesField(out, 30, marshalTimestamp(now.Add(366*24*time.Hour)))
	out = appendBytesField(out, 31, marshalTimestamp(now.Add(367*24*time.Hour)))
	return out
}

func appendApplicationDataInt(out []byte, key string, value uint64) []byte {
	item := appendStringField(nil, 1, key)
	item = appendBytesField(item, 2, appendVarintField(nil, 3, value))
	return appendBytesField(out, 1, item)
}

func appendApplicationDataString(out []byte, key, value string) []byte {
	item := appendStringField(nil, 1, key)
	item = appendBytesField(item, 2, appendStringField(nil, 7, value))
	return appendBytesField(out, 1, item)
}

func appendApplicationDataBytes(out []byte, key string, value []byte) []byte {
	item := appendStringField(nil, 1, key)
	item = appendBytesField(item, 2, appendBytesField(nil, 8, value))
	return appendBytesField(out, 1, item)
}

func marshalVioletCompetitionApplicationData(rulePreset, controlTime, totalTime uint64, hostName string) []byte {
	out := appendApplicationDataInt(nil, "RulePresetNo", rulePreset)
	out = appendApplicationDataInt(out, "ControlTimeOverride", controlTime)
	out = appendApplicationDataInt(out, "HostPlayerSex", 1)
	out = appendApplicationDataInt(out, "HostPlayerLang", 2)
	out = appendApplicationDataString(out, "HostPlayerName", hostName)
	return appendApplicationDataInt(out, "TotalTimeOverride", totalTime)
}

func marshalOfficialCompetition(name string, now time.Time) ([]byte, error) {
	// Official Rules 1 selects local preset 10. Unlike the friendly-host detail
	// screen, the official-search callback immediately constructs a regulation
	// holder and requires the complete native 0x29c0-byte record under the
	// title-owned application_data["Regulation"] key.
	regulation, err := loadVioletRegulationRecord(10)
	if err != nil {
		return nil, err
	}
	out := appendStringField(nil, 1, name)
	out = appendStringField(out, 2, competitionAliasName("OFC001"))
	out = appendVarintField(out, 33, 1) // OFFICIAL.
	out = appendVarintField(out, 5, 1)
	out = appendStringMapField(out, 6, "en", "Nextendo Online Competition")
	out = appendStringMapField(out, 7, "en", "Single Battle")
	out = appendVarintField(out, 8, 3) // ANYBODY.
	out = appendVarintField(out, 9, 1) // OWNER.
	out = appendVarintField(out, 10, 1)
	out = appendBytesField(out, 11, protowire.AppendVarint(nil, 399))
	out = appendBytesField(out, 34, []byte{1, 2, 3})
	out = appendVarintField(out, 14, 1)
	out = appendVarintField(out, 15, 100000)
	out = appendVarintField(out, 16, 1)
	// Violet parses this field as a resource name immediately after search.
	// A short identifier reaches main+0x4f6284 and aborts because the expected
	// `matchmakingConfigs` collection segment is absent.
	out = appendStringField(out, 17, nplnTenant+"/matchmakingConfigs/Competition")
	out = appendVarintField(out, 18, 1) // SERVER creates matching keys.
	hours := make([]byte, 0, 24)
	for hour := uint64(0); hour < 24; hour++ {
		hours = protowire.AppendVarint(hours, hour)
	}
	out = appendBytesField(out, 19, hours)
	out = appendVarintField(out, 20, 9999)
	out = appendVarintField(out, 21, 9999)
	out = appendVarintField(out, 35, 1)
	applicationData := marshalVioletCompetitionApplicationData(10, 7, 20, "Nextendo")
	applicationData = appendApplicationDataBytes(applicationData, "Regulation", regulation)
	out = appendBytesField(out, 24, applicationData)
	out = appendBytesField(out, 25, marshalTimestamp(now.Add(-48*time.Hour)))
	out = appendBytesField(out, 36, marshalTimestamp(now.Add(-24*time.Hour)))
	out = appendBytesField(out, 26, marshalTimestamp(now.Add(-24*time.Hour)))
	out = appendBytesField(out, 27, marshalTimestamp(now.Add(24*time.Hour)))
	// Keep the synthetic local event in an active battle window. The previous
	// future-only window correctly exercised registration, but prevented the
	// remaining battle and result lifecycle from being tested.
	out = appendBytesField(out, 28, marshalTimestamp(now.Add(-1*time.Hour)))
	out = appendBytesField(out, 29, marshalTimestamp(now.Add(48*time.Hour)))
	out = appendBytesField(out, 30, marshalTimestamp(now.Add(49*time.Hour)))
	return appendBytesField(out, 31, marshalTimestamp(now.Add(50*time.Hour))), nil
}

func (s *competitionServer) GetCompetition(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	_, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	name, err := parseProtoStringMessage(message.b)
	if err != nil {
		return nil, err
	}
	canonicalName, canonicalErr := canonicalHostedCompetitionName(name)
	if canonicalErr == nil {
		s.mu.Lock()
		hosted, ok := s.hosted[canonicalName]
		s.mu.Unlock()
		if ok {
			log.Printf("[NPLN Timber] GetCompetition name=%q type=FRIENDLY", canonicalName)
			return &rawMsg{b: append([]byte(nil), hosted.competition...)}, nil
		}
	}
	if officialName, officialErr := canonicalOfficialCompetitionName(name); officialErr == nil {
		competition, err := marshalOfficialCompetition(officialName, time.Now().UTC())
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		log.Printf("[NPLN Timber] GetCompetition requested=%q name=%q type=OFFICIAL", name, officialName)
		return &rawMsg{b: competition}, nil
	}
	competitionType, err := rankedCompetitionTypeFromName(name)
	if err != nil {
		return nil, err
	}
	resourceMode := strings.ToLower(strings.TrimSpace(os.Getenv("VIOLET_RANKED_RESOURCE_MODE")))
	if resourceMode == "regulated" {
		wire, err := marshalRankedCompetitionRegulated(name, competitionType, time.Now().UTC())
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		log.Printf("[NPLN Timber] GetCompetition type=%d mode=regulated", competitionType)
		return &rawMsg{b: wire}, nil
	}
	if resourceMode == "minimal" {
		log.Printf("[NPLN Timber] GetCompetition name=%q type=%d view=BASIC mode=minimal-diagnostic", name, competitionType)
		return &rawMsg{b: marshalRankedCompetitionMinimal(name, competitionType)}, nil
	}
	if resourceMode == "full-diagnostic" {
		log.Printf("[NPLN Timber] GetCompetition name=%q type=%d view=BASIC mode=full-diagnostic", name, competitionType)
		return &rawMsg{b: marshalRankedCompetitionNamed(name, competitionType, time.Now().UTC())}, nil
	}
	// The scheduled profile is the only resource shape verified by Violet. The
	// broader synthetic Competition (especially application_data and
	// matchmaking_config_name) makes nn.npln.Worker abort before the menu opens.
	log.Printf("[NPLN Timber] GetCompetition name=%q type=%d view=BASIC mode=scheduled", name, competitionType)
	return &rawMsg{b: marshalRankedCompetitionScheduled(name, competitionType, time.Now().UTC())}, nil
}

func (s *competitionServer) SearchCompetitions(ctx context.Context, message *rawMsg) (*rawMsg, error) {
	if _, err := authenticatedNPLNUID(ctx); err != nil {
		return nil, err
	}
	request, err := parseSearchCompetitionsRequest(message.b)
	if err != nil {
		return nil, err
	}
	if len(request.types) == 1 && request.types[0] == 1 {
		competition, err := marshalOfficialCompetition(officialCompetitionName(), time.Now().UTC())
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		log.Printf("[NPLN Timber] SearchCompetitions type=OFFICIAL page_size=%d results=1", request.pageSize)
		return &rawMsg{b: appendBytesField(nil, 1, competition)}, nil
	}
	// A zero-result response is a valid discovery outcome and gives us a safe
	// diagnostic boundary: if Violet accepts it, the transport and response
	// envelope are sound and the abort is caused by one of the synthetic
	// Competition fields below. Keep this opt-in so the full implementation
	// remains covered by the normal wire-contract tests.
	discoveryMode := strings.ToLower(strings.TrimSpace(os.Getenv("VIOLET_RANKED_DISCOVERY_MODE")))
	if discoveryMode == "empty" {
		log.Printf("[NPLN Timber] SearchCompetitions view=%d page_size=%d types=%v results=0 mode=empty-diagnostic", request.view, request.pageSize, request.types)
		return &rawMsg{}, nil
	}
	now := time.Now().UTC()
	out := []byte(nil)
	if discoveryMode == "regulated" {
		for _, competitionType := range request.types {
			competition, err := marshalRankedCompetitionRegulated(rankedCompetitionName(competitionType), competitionType, now)
			if err != nil {
				return nil, status.Error(codes.FailedPrecondition, err.Error())
			}
			out = appendBytesField(out, 1, competition)
		}
		log.Printf("[NPLN Timber] SearchCompetitions types=%v mode=regulated", request.types)
		return &rawMsg{b: out}, nil
	}
	if discoveryMode != "full-diagnostic" {
		for _, competitionType := range request.types {
			competition := marshalRankedCompetitionScheduled(rankedCompetitionName(competitionType), competitionType, now)
			out = appendBytesField(out, 1, competition)
		}
		log.Printf("[NPLN Timber] SearchCompetitions view=%d page_size=%d types=%v results=%d mode=scheduled", request.view, request.pageSize, request.types, len(request.types))
		return &rawMsg{b: out}, nil
	}
	for _, competitionType := range request.types {
		out = appendBytesField(out, 1, marshalRankedCompetition(competitionType, now))
	}
	log.Printf("[NPLN Timber] SearchCompetitions view=%d page_size=%d types=%v results=%d mode=full-diagnostic", request.view, request.pageSize, request.types, len(request.types))
	return &rawMsg{b: out}, nil
}

type timberCompetitionService interface{}

// This is a server-streaming subscription, not a unary acknowledgement.
// Violet's receive loop (main+0x5e5b6c) ignores an unset response oneof.
// Case 2 contains CompetitionNotification, whose case 4 carries matching_key.
// It consumes this directly; an empty message does not trigger a Get RPC.
func (s *competitionServer) receiveCompetitionNotification(stream grpc.ServerStream) error {
	uid, err := authenticatedNPLNUID(stream.Context())
	if err != nil {
		return err
	}
	request := new(rawMsg)
	if err := stream.RecvMsg(request); err != nil {
		return err
	}
	name, err := parseProtoStringMessage(request.b)
	if err != nil {
		return status.Error(codes.InvalidArgument, "invalid notification request")
	}
	competition, err := rankedNotificationCompetition(name)
	if err != nil {
		return err
	}
	parts := strings.Split(name, "/")
	if parts[5] != "current" && parts[5] != uid {
		return status.Error(codes.PermissionDenied, "notification participant does not match caller")
	}
	if err := stream.SendHeader(metadata.MD{}); err != nil {
		return err
	}
	log.Printf("[NPLN Timber] ReceiveCompetitionNotification subscribed name=%q", name)
	wake := make(chan struct{})
	s.mu.Lock()
	queue := s.waiters[competition]
	active := queue[:0]
	for _, waiter := range queue {
		if waiter.ctx.Err() == nil {
			active = append(active, waiter)
		}
	}
	queue = active
	s.waiters[competition] = queue
	for _, waiter := range queue {
		if waiter.uid == uid {
			s.mu.Unlock()
			return status.Error(codes.AlreadyExists, "participant already waiting")
		}
	}
	paired := -1
	for i, waiter := range queue {
		if waiter.uid != uid {
			paired = i
			break
		}
	}
	if paired >= 0 {
		key, keyErr := newResourceUUID()
		if keyErr != nil {
			s.mu.Unlock()
			return keyErr
		}
		now := time.Now().UTC()
		other := queue[paired]
		s.notifications[rankedNotificationKey(other.uid, competition)] = rankedNotification{matchingKey: key, etag: key, updateTime: now}
		s.notifications[rankedNotificationKey(uid, competition)] = rankedNotification{matchingKey: key, etag: key, updateTime: now}
		queue = append(queue[:paired], queue[paired+1:]...)
		close(other.wake)
		close(wake)
		s.waiters[competition] = queue
		log.Printf("[NPLN Timber] Ranked pair formed competition=%q users=%q,%q matching_key=%q", competition, other.uid, uid, key)
	} else {
		s.waiters[competition] = append(queue, &rankedNotificationWaiter{uid: uid, wake: wake, ctx: stream.Context()})
	}
	s.mu.Unlock()
	select {
	case <-wake:
		s.mu.Lock()
		notification := s.notifications[rankedNotificationKey(uid, competition)]
		s.mu.Unlock()
		payload := marshalCompetitionNotification(name, notification.matchingKey, notification.etag, notification.updateTime)
		if err := stream.SendMsg(&rawMsg{b: appendBytesField(nil, 2, payload)}); err != nil {
			return err
		}
		log.Printf("[NPLN Timber] ReceiveCompetitionNotification matching notification sent uid=%q field=2 bytes=%d", uid, len(payload))
		// Violet awaits this Receive operation before it starts the title-side
		// search keyed by matching_key.  Keeping the server stream alive after
		// the one matching notification leaves that operation pending forever:
		// its message callback runs, but the Lua flow never advances to the
		// shared Casual/Ranked search engine.  This RPC is therefore one-shot
		// for a queued participant and must finish successfully after delivery.
		log.Printf("[NPLN Timber] ReceiveCompetitionNotification completed uid=%q after matching delivery", uid)
		return nil
	case <-stream.Context().Done():
		s.mu.Lock()
		queue := s.waiters[competition]
		for i, waiter := range queue {
			if waiter.uid == uid && waiter.wake == wake {
				s.waiters[competition] = append(queue[:i], queue[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
	}
	log.Printf("[NPLN Timber] ReceiveCompetitionNotification closed name=%q", name)
	return status.FromContextError(stream.Context().Err()).Err()
}

func competitionUnaryHandler(fullMethod string, call func(*competitionServer, context.Context, *rawMsg) (*rawMsg, error)) grpc.MethodDesc {
	methodName := fullMethod[strings.LastIndexByte(fullMethod, '/')+1:]
	return grpc.MethodDesc{MethodName: methodName, Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		in := new(rawMsg)
		if err := dec(in); err != nil {
			return nil, err
		}
		if interceptor == nil {
			return call(srv.(*competitionServer), ctx, in)
		}
		info := &grpc.UnaryServerInfo{Server: srv, FullMethod: fullMethod}
		handler := func(ctx context.Context, req any) (any, error) {
			return call(srv.(*competitionServer), ctx, req.(*rawMsg))
		}
		return interceptor(ctx, in, info, handler)
	}}
}

var competitionServiceDesc = grpc.ServiceDesc{
	ServiceName: competitionServiceName,
	HandlerType: (*timberCompetitionService)(nil),
	Streams: []grpc.StreamDesc{{
		StreamName:    "ReceiveCompetitionNotification",
		ServerStreams: true,
		Handler: func(srv any, stream grpc.ServerStream) error {
			return srv.(*competitionServer).receiveCompetitionNotification(stream)
		},
	}},
	Methods: []grpc.MethodDesc{
		competitionUnaryHandler("/"+competitionServiceName+"/GetCompetition", (*competitionServer).GetCompetition),
		competitionUnaryHandler("/"+competitionServiceName+"/GetCompetitionAlias", (*competitionServer).GetCompetitionAlias),
		competitionUnaryHandler("/"+competitionServiceName+"/GetCompetitionUser", (*competitionServer).GetCompetitionUser),
		competitionUnaryHandler("/"+competitionServiceName+"/UpdateCompetitionUser", (*competitionServer).UpdateCompetitionUser),
		competitionUnaryHandler("/"+competitionServiceName+"/HostCompetition", (*competitionServer).HostCompetition),
		competitionUnaryHandler("/"+competitionServiceName+"/SearchCompetitions", (*competitionServer).SearchCompetitions),
		competitionUnaryHandler("/"+competitionServiceName+"/CreateCompetitionParticipant", (*competitionServer).CreateCompetitionParticipant),
		competitionUnaryHandler("/"+competitionServiceName+"/ActivateCompetitionParticipant", (*competitionServer).ActivateCompetitionParticipant),
		competitionUnaryHandler("/"+competitionServiceName+"/GetCompetitionParticipant", (*competitionServer).GetCompetitionParticipant),
		competitionUnaryHandler("/"+competitionServiceName+"/GetCompetitionNotification", (*competitionServer).GetCompetitionNotification),
	},
}

func registerCompetitionServer(server grpc.ServiceRegistrar, implementation *competitionServer) {
	server.RegisterService(&competitionServiceDesc, implementation)
}
