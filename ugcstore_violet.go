package main

import (
	"context"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	commonpb "npln.nintendo.net/npln-practice/proto/common"
	ugcpb "npln.nintendo.net/npln-practice/proto/ugcstore/v1"
)

const violetRentalTeamBinarySize = 0x84e

// violetUgcstoreServer implements the bounded UGC families observed in Violet:
// the per-caller penalty document and the five Rental Team slots. It contains
// no Wonder course storage and no Splatoon game-record policy.
type violetUgcstoreServer struct {
	ugcpb.UnimplementedUgcstoreServer
	mu sync.Mutex
	// Local UGC aliases are not bearer identities. Isolate storage by verified
	// caller, including when copied saves supply the same opaque alias.
	penalties map[string]*ugcpb.Document
	// Rental documents are public through short aliases, but only the caller
	// that first publishes a slot may update or delete it.
	rentalDocuments map[string]*ugcpb.Document
	rentalOwners    map[string]string
	rentalAliases   map[string]*ugcpb.DocumentShortAlias
	nextRentalAlias uint64
}

func canonicalVioletUGCName(name string) string {
	return strings.Replace(name, "tenants/current/", nplnTenant+"/", 1)
}

func (u *violetUgcstoreServer) ensureMapsLocked() {
	if u.penalties == nil {
		u.penalties = make(map[string]*ugcpb.Document)
	}
	if u.rentalDocuments == nil {
		u.rentalDocuments = make(map[string]*ugcpb.Document)
		u.rentalOwners = make(map[string]string)
		u.rentalAliases = make(map[string]*ugcpb.DocumentShortAlias)
	}
}

func validVioletPenaltyDocumentName(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) != 7 || parts[0] != "tenants" || (parts[1] != "current" && parts[1] != nplnTenantID) ||
		parts[2] != "documents" || parts[3] != "users" || parts[5] != "privateItems" || parts[6] != "penalty" {
		return false
	}
	// The captured SDK document owner is a stable UGC identity distinct from
	// the NPLN bearer subject. Authenticate the caller, then validate the opaque
	// owner as one safe resource segment instead of incorrectly equating them.
	owner := parts[4]
	if !strings.HasPrefix(owner, "u-") || len(owner) < 3 {
		return false
	}
	for _, c := range owner[2:] {
		if c < 'a' || c > 'z' {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func validVioletRentalDocumentName(name string) bool {
	parts := strings.Split(name, "/")
	if len(parts) != 7 || parts[0] != "tenants" || (parts[1] != "current" && parts[1] != nplnTenantID) ||
		parts[2] != "documents" || parts[3] != "users" || parts[5] != "publicItems" {
		return false
	}
	if !strings.HasPrefix(parts[4], "u-") || len(parts[4]) < 3 {
		return false
	}
	if !strings.HasPrefix(parts[6], "rental_team_") || len(parts[6]) != len("rental_team_0") {
		return false
	}
	return parts[6][len(parts[6])-1] >= '0' && parts[6][len(parts[6])-1] <= '4'
}

func validateVioletRentalFields(fields map[string]*commonpb.Value, requireBinary bool) error {
	if len(fields) == 0 || len(fields) > 2 {
		return status.Error(codes.InvalidArgument, "rental team requires its bounded Violet fields")
	}
	for key, value := range fields {
		switch key {
		case "RentalTeamBinary":
			if _, ok := value.GetValueType().(*commonpb.Value_BytesValue); !ok || len(value.GetBytesValue()) != violetRentalTeamBinarySize {
				return status.Errorf(codes.InvalidArgument, "RentalTeamBinary must be exactly %d bytes", violetRentalTeamBinarySize)
			}
		case "RentalTeamDownloadCode":
			if _, ok := value.GetValueType().(*commonpb.Value_StringValue); !ok {
				return status.Error(codes.InvalidArgument, "RentalTeamDownloadCode must be a string")
			}
			code := value.GetStringValue()
			if len(code) > 32 {
				return status.Error(codes.InvalidArgument, "RentalTeamDownloadCode is too long")
			}
		default:
			return status.Errorf(codes.InvalidArgument, "unknown rental-team field %q", key)
		}
	}
	if requireBinary && fields["RentalTeamBinary"] == nil {
		return status.Error(codes.InvalidArgument, "RentalTeamBinary is required")
	}
	return nil
}

func (u *violetUgcstoreServer) GetDocument(ctx context.Context, req *ugcpb.GetDocumentRequest) (*ugcpb.Document, error) {
	uid, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.ensureMapsLocked()
	name := canonicalVioletUGCName(req.Name)
	if validVioletRentalDocumentName(req.GetName()) {
		stored := u.rentalDocuments[name]
		if stored == nil {
			return nil, status.Error(codes.NotFound, "rental team is not published")
		}
		out := proto.Clone(stored).(*ugcpb.Document)
		out.Name = req.Name
		return out, nil
	}
	// Do not let a penalty document already bound to this bearer turn an
	// unrelated UGC lookup into PermissionDenied. Violet bulk-reads the five
	// public Rental Team slots after it has fetched its penalty document.
	if !validVioletPenaltyDocumentName(req.GetName()) {
		return nil, status.Error(codes.Unimplemented, "only Violet's observed UGC documents are available")
	}
	if stored := u.penalties[uid]; stored != nil {
		if stored.Name != name {
			return nil, status.Error(codes.PermissionDenied, "penalty alias differs from caller binding")
		}
		out := proto.Clone(stored).(*ugcpb.Document)
		out.Name = req.Name
		return out, nil
	}
	now := timestamppb.Now()
	doc := &ugcpb.Document{
		Name: name, CreateTime: now, UpdateTime: now,
		Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"disconnect_count": gamesyncIntegerValue(0),
			"penalty_start_at": gamesyncIntegerValue(0),
			"warning_flag":     {ValueType: &commonpb.Value_BooleanValue{BooleanValue: false}},
		}},
	}
	u.penalties[uid] = doc
	out := proto.Clone(doc).(*ugcpb.Document)
	out.Name = req.Name
	return out, nil
}

func (u *violetUgcstoreServer) CommitDocuments(ctx context.Context, req *ugcpb.CommitDocumentsRequest) (*ugcpb.CommitDocumentsResponse, error) {
	uid, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetTenant() != "tenants/current" && req.GetTenant() != nplnTenant {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant")
	}
	if len(req.GetWriteOperations()) != 1 || req.GetRuleContext() != nil {
		return nil, status.Error(codes.Unimplemented, "only one bounded Violet UGC update is supported")
	}
	operation := req.WriteOperations[0]
	if deletion := operation.GetDeleteDocument(); deletion != nil {
		if !validVioletRentalDocumentName(deletion.GetName()) {
			return nil, status.Error(codes.PermissionDenied, "only rental teams may be deleted")
		}
		name := canonicalVioletUGCName(deletion.GetName())
		u.mu.Lock()
		defer u.mu.Unlock()
		u.ensureMapsLocked()
		if owner := u.rentalOwners[name]; owner != "" && owner != uid {
			return nil, status.Error(codes.PermissionDenied, "rental team belongs to another caller")
		}
		delete(u.rentalDocuments, name)
		delete(u.rentalOwners, name)
		for code, alias := range u.rentalAliases {
			if alias.GetDocument() == name {
				delete(u.rentalAliases, code)
			}
		}
		now := timestamppb.Now()
		return &ugcpb.CommitDocumentsResponse{CommitTime: now, WriteResults: []*ugcpb.WriteResult{{UpdateTime: now}}}, nil
	}
	update := req.WriteOperations[0].GetUpdateDocument()
	if update == nil {
		return nil, status.Error(codes.Unimplemented, "unsupported operation or precondition")
	}
	doc := update.GetDocument()
	if validVioletRentalDocumentName(doc.GetName()) {
		fields := doc.GetFields().GetFields()
		if err := validateVioletRentalFields(fields, false); err != nil {
			return nil, err
		}
		name := canonicalVioletUGCName(doc.GetName())
		u.mu.Lock()
		defer u.mu.Unlock()
		u.ensureMapsLocked()
		if owner := u.rentalOwners[name]; owner != "" && owner != uid {
			return nil, status.Error(codes.PermissionDenied, "rental team belongs to another caller")
		}
		stored := u.rentalDocuments[name]
		paths := update.GetUpdateMask().GetPaths()
		isFullWrite := len(paths) == 1 && paths[0] == "*"
		isCodePatch := len(paths) == 1 && paths[0] == "`RentalTeamDownloadCode`"
		if !isFullWrite && !isCodePatch {
			return nil, status.Error(codes.Unimplemented, "unsupported rental-team update mask")
		}
		if stored == nil {
			if !isFullWrite || fields["RentalTeamBinary"] == nil {
				return nil, status.Error(codes.InvalidArgument, "RentalTeamBinary is required for initial publication")
			}
		} else if isCodePatch {
			if len(fields) != 1 || fields["RentalTeamDownloadCode"] == nil {
				return nil, status.Error(codes.InvalidArgument, "download-code patch contains unexpected fields")
			}
			code := fields["RentalTeamDownloadCode"].GetStringValue()
			alias := u.rentalAliases[code]
			if alias == nil || alias.GetDocument() != name {
				return nil, status.Error(codes.InvalidArgument, "rental download code does not identify this team")
			}
		} else if fields["RentalTeamBinary"] == nil {
			return nil, status.Error(codes.InvalidArgument, "RentalTeamBinary is required for full publication")
		}
		now := timestamppb.Now()
		createTime := now
		outFields := proto.Clone(doc.Fields).(*commonpb.MapValue)
		if stored != nil {
			createTime = stored.CreateTime
			if isCodePatch {
				outFields = proto.Clone(stored.Fields).(*commonpb.MapValue)
				outFields.Fields["RentalTeamDownloadCode"] = proto.Clone(fields["RentalTeamDownloadCode"]).(*commonpb.Value)
			}
		}
		out := &ugcpb.Document{Name: name, Fields: outFields, CreateTime: createTime, UpdateTime: now}
		u.rentalDocuments[name] = out
		u.rentalOwners[name] = uid
		return &ugcpb.CommitDocumentsResponse{CommitTime: now, WriteResults: []*ugcpb.WriteResult{{UpdateTime: now}}}, nil
	}
	if update.CurrentDocument != nil {
		return nil, status.Error(codes.Unimplemented, "unsupported penalty precondition")
	}
	if !validVioletPenaltyDocumentName(doc.GetName()) {
		return nil, status.Error(codes.PermissionDenied, "not a penalty document")
	}
	fields := doc.GetFields().GetFields()
	for key, value := range fields {
		switch key {
		case "disconnect_count", "penalty_start_at":
			v, ok := value.GetValueType().(*commonpb.Value_IntegerValue)
			if !ok || v.IntegerValue < 0 {
				return nil, status.Error(codes.InvalidArgument, "penalty integers must be nonnegative")
			}
		case "warning_flag":
			if _, ok := value.GetValueType().(*commonpb.Value_BooleanValue); !ok {
				return nil, status.Error(codes.InvalidArgument, "warning_flag must be boolean")
			}
		default:
			return nil, status.Error(codes.InvalidArgument, "unknown penalty field")
		}
	}
	paths := update.GetUpdateMask().GetPaths()
	if len(paths) == 0 || (len(paths) == 1 && paths[0] == "*") {
		if len(fields) != 3 {
			return nil, status.Error(codes.InvalidArgument, "complete penalty document required")
		}
		paths = []string{"disconnect_count", "penalty_start_at", "warning_flag"}
	}
	for _, key := range paths {
		if fields[key] == nil {
			return nil, status.Error(codes.InvalidArgument, "mask must select supplied penalty fields")
		}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.ensureMapsLocked()
	stored := u.penalties[uid]
	name := canonicalVioletUGCName(doc.Name)
	if stored == nil {
		// A backend restart can occur while Violet retains its authenticated
		// online session. A complete, validated penalty update is sufficient to
		// restore the caller-local alias; storage remains isolated by bearer UID.
		now := timestamppb.Now()
		stored = &ugcpb.Document{Name: name, CreateTime: now, UpdateTime: now, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
			"disconnect_count": gamesyncIntegerValue(0),
			"penalty_start_at": gamesyncIntegerValue(0),
			"warning_flag":     {ValueType: &commonpb.Value_BooleanValue{BooleanValue: false}},
		}}}
	}
	if stored.Name != name {
		return nil, status.Error(codes.PermissionDenied, "read and bind caller penalty document before updating")
	}
	out := proto.Clone(stored).(*ugcpb.Document)
	for _, key := range paths {
		out.Fields.Fields[key] = proto.Clone(fields[key]).(*commonpb.Value)
	}
	now := timestamppb.Now()
	out.UpdateTime = now
	u.penalties[uid] = out
	return &ugcpb.CommitDocumentsResponse{CommitTime: now, WriteResults: []*ugcpb.WriteResult{{UpdateTime: now}}}, nil
}

func (u *violetUgcstoreServer) BulkGetDocuments(req *ugcpb.BulkGetDocumentsRequest, stream grpc.ServerStreamingServer[ugcpb.BulkGetDocumentsResponse]) error {
	if _, err := authenticatedNPLNUID(stream.Context()); err != nil {
		return err
	}
	for _, requested := range req.GetNames() {
		name := requested
		if !strings.HasPrefix(name, "tenants/") {
			name = strings.TrimSuffix(req.GetParent(), "/") + "/" + strings.TrimPrefix(name, "/")
		}
		doc, err := u.GetDocument(stream.Context(), &ugcpb.GetDocumentRequest{Name: name, ReadMask: req.GetReadMask(), RuleContext: req.GetRuleContext()})
		response := &ugcpb.BulkGetDocumentsResponse{ReadTime: timestamppb.Now()}
		if status.Code(err) == codes.NotFound {
			response.Result = &ugcpb.BulkGetDocumentsResponse_Missing{Missing: requested}
		} else if err != nil {
			return err
		} else {
			response.Result = &ugcpb.BulkGetDocumentsResponse_Found{Found: doc}
		}
		if err := stream.Send(response); err != nil {
			return err
		}
	}
	return nil
}

func rentalAliasCode(value uint64) string {
	const alphabet = "0123456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	code := [6]byte{}
	for i := len(code) - 1; i >= 0; i-- {
		code[i] = alphabet[value%uint64(len(alphabet))]
		value /= uint64(len(alphabet))
	}
	return string(code[:])
}

func rentalAliasSegments(name string) (string, string, bool) {
	parts := strings.Split(name, "/")
	if len(parts) != 6 || parts[0] != "tenants" || (parts[1] != "current" && parts[1] != nplnTenantID) ||
		parts[2] != "scopes" || parts[3] != "length6" || parts[4] != "documentShortAliases" {
		return "", "", false
	}
	code := strings.ToUpper(parts[5])
	if len(code) != 6 {
		return "", "", false
	}
	for _, c := range code {
		if !strings.ContainsRune("0123456789ABCDEFGHJKLMNPQRSTUVWXYZ", c) {
			return "", "", false
		}
	}
	return parts[3], code, true
}

func (u *violetUgcstoreServer) CreateDocumentShortAlias(ctx context.Context, req *ugcpb.CreateDocumentShortAliasRequest) (*ugcpb.DocumentShortAlias, error) {
	uid, err := authenticatedNPLNUID(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetRuleContext() != nil || (req.GetParent() != "tenants/current" && req.GetParent() != nplnTenant) {
		return nil, status.Error(codes.InvalidArgument, "invalid rental alias parent")
	}
	requested := req.GetDocumentShortAlias()
	document := canonicalVioletUGCName(requested.GetDocument())
	if !validVioletRentalDocumentName(document) {
		return nil, status.Error(codes.InvalidArgument, "alias target is not a Violet rental team")
	}
	if requested.GetScope() != "length6" {
		return nil, status.Error(codes.InvalidArgument, "Violet rental aliases require scope length6")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.ensureMapsLocked()
	if u.rentalDocuments[document] == nil || u.rentalOwners[document] != uid {
		return nil, status.Error(codes.PermissionDenied, "publish the caller's rental team before creating an alias")
	}
	var code string
	if requested.GetName() != "" {
		var ok bool
		var scope string
		scope, code, ok = rentalAliasSegments(requested.GetName())
		if !ok || scope != requested.GetScope() {
			return nil, status.Error(codes.InvalidArgument, "invalid rental download code")
		}
		if u.rentalAliases[code] != nil {
			return nil, status.Error(codes.AlreadyExists, "rental download code already exists")
		}
	} else {
		for {
			u.nextRentalAlias++
			code = rentalAliasCode(u.nextRentalAlias)
			if u.rentalAliases[code] == nil {
				break
			}
		}
	}
	alias := &ugcpb.DocumentShortAlias{
		Name:     nplnTenant + "/scopes/" + requested.GetScope() + "/documentShortAliases/" + code,
		Document: document, Scope: requested.GetScope(),
	}
	u.rentalAliases[code] = alias
	return proto.Clone(alias).(*ugcpb.DocumentShortAlias), nil
}

func (u *violetUgcstoreServer) GetDocumentShortAlias(ctx context.Context, req *ugcpb.GetDocumentShortAliasRequest) (*ugcpb.DocumentShortAlias, error) {
	if _, err := authenticatedNPLNUID(ctx); err != nil {
		return nil, err
	}
	scope, code, ok := rentalAliasSegments(req.GetName())
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "invalid rental download code")
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.ensureMapsLocked()
	alias := u.rentalAliases[code]
	if alias == nil {
		return nil, status.Error(codes.NotFound, "rental download code not found")
	}
	out := proto.Clone(alias).(*ugcpb.DocumentShortAlias)
	if strings.HasPrefix(req.GetName(), "tenants/current/") {
		out.Name = "tenants/current/scopes/" + scope + "/documentShortAliases/" + code
	}
	return out, nil
}

func (u *violetUgcstoreServer) BulkGetDocumentShortAliases(ctx context.Context, req *ugcpb.BulkGetDocumentShortAliasesRequest) (*ugcpb.BulkGetDocumentShortAliasesResponse, error) {
	if _, err := authenticatedNPLNUID(ctx); err != nil {
		return nil, err
	}
	response := &ugcpb.BulkGetDocumentShortAliasesResponse{}
	for _, item := range req.GetRequests() {
		alias, err := u.GetDocumentShortAlias(ctx, item)
		result := &ugcpb.BulkGetDocumentShortAliasesResult{}
		if status.Code(err) == codes.NotFound {
			result.Result = &ugcpb.BulkGetDocumentShortAliasesResult_Missing{Missing: item.GetName()}
		} else if err != nil {
			return nil, err
		} else {
			result.Result = &ugcpb.BulkGetDocumentShortAliasesResult_Found{Found: alias}
		}
		response.Results = append(response.Results, result)
	}
	return response, nil
}
