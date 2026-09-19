package main

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	commonpb "npln.nintendo.net/npln-practice/proto/common"
	ugcpb "npln.nintendo.net/npln-practice/proto/ugcstore/v1"
)

func rentalTeamDocument(name string, size int) *ugcpb.Document {
	return &ugcpb.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"RentalTeamBinary": {
			ValueType: &commonpb.Value_BytesValue{BytesValue: make([]byte, size)},
		},
		"RentalTeamDownloadCode": {
			ValueType: &commonpb.Value_StringValue{StringValue: ""},
		},
	}}}
}

func rentalTeamCommit(doc *ugcpb.Document) *ugcpb.CommitDocumentsRequest {
	return &ugcpb.CommitDocumentsRequest{
		Tenant: "tenants/current",
		WriteOperations: []*ugcpb.WriteOperation{{OperationType: &ugcpb.WriteOperation_UpdateDocument{
			UpdateDocument: &ugcpb.UpdateDocumentRequest{Document: doc, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"*"}}},
		}}},
	}
}

func TestRentalTeamDownloadCodePatchPreservesBinary(t *testing.T) {
	server := &violetUgcstoreServer{}
	owner := violetAuthenticatedContext("u-owner")
	name := "tenants/current/documents/users/u-local/publicItems/rental_team_0"
	if _, err := server.CommitDocuments(owner, rentalTeamCommit(rentalTeamDocument(name, violetRentalTeamBinarySize))); err != nil {
		t.Fatal(err)
	}
	alias, err := server.CreateDocumentShortAlias(owner, &ugcpb.CreateDocumentShortAliasRequest{
		Parent: "tenants/current", DocumentShortAlias: &ugcpb.DocumentShortAlias{Document: name, Scope: "length6"},
	})
	if err != nil {
		t.Fatal(err)
	}
	code := lastResourceSegment(alias.GetName())
	patch := &ugcpb.CommitDocumentsRequest{Tenant: "tenants/current", WriteOperations: []*ugcpb.WriteOperation{{
		OperationType: &ugcpb.WriteOperation_UpdateDocument{UpdateDocument: &ugcpb.UpdateDocumentRequest{
			Document: &ugcpb.Document{Name: name, Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
				"RentalTeamDownloadCode": {ValueType: &commonpb.Value_StringValue{StringValue: code}},
			}}},
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"`RentalTeamDownloadCode`"}},
		}},
	}}}
	if _, err := server.CommitDocuments(owner, patch); err != nil {
		t.Fatal(err)
	}
	stored, err := server.GetDocument(owner, &ugcpb.GetDocumentRequest{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	fields := stored.GetFields().GetFields()
	if got := len(fields["RentalTeamBinary"].GetBytesValue()); got != violetRentalTeamBinarySize {
		t.Fatalf("binary after code patch = %d bytes", got)
	}
	if got := fields["RentalTeamDownloadCode"].GetStringValue(); got != code {
		t.Fatalf("download code = %q, want %q", got, code)
	}
}

func TestRentalTeamPublishAliasAndDownload(t *testing.T) {
	server := &violetUgcstoreServer{}
	owner := violetAuthenticatedContext("u-owner")
	renter := violetAuthenticatedContext("u-renter")
	name := "tenants/current/documents/users/u-local/publicItems/rental_team_0"

	if _, err := server.GetDocument(owner, &ugcpb.GetDocumentRequest{Name: name}); status.Code(err) != codes.NotFound {
		t.Fatalf("unpublished rental slot = %v, want NotFound", err)
	}
	if _, err := server.CommitDocuments(owner, rentalTeamCommit(rentalTeamDocument(name, violetRentalTeamBinarySize))); err != nil {
		t.Fatal(err)
	}
	alias, err := server.CreateDocumentShortAlias(owner, &ugcpb.CreateDocumentShortAliasRequest{
		Parent:             "tenants/current",
		DocumentShortAlias: &ugcpb.DocumentShortAlias{Document: name, Scope: "length6"},
	})
	if err != nil {
		t.Fatal(err)
	}
	code := lastResourceSegment(alias.GetName())
	if len(code) != 6 {
		t.Fatalf("download code %q is not six characters", code)
	}
	resolved, err := server.GetDocumentShortAlias(renter, &ugcpb.GetDocumentShortAliasRequest{
		Name: "tenants/current/scopes/length6/documentShortAliases/" + code,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.GetDocument() != canonicalVioletUGCName(name) {
		t.Fatalf("alias target = %q", resolved.GetDocument())
	}
	downloaded, err := server.GetDocument(renter, &ugcpb.GetDocumentRequest{Name: resolved.GetDocument()})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(downloaded.GetFields().GetFields()["RentalTeamBinary"].GetBytesValue()); got != violetRentalTeamBinarySize {
		t.Fatalf("downloaded rental binary = %d bytes", got)
	}
	if _, err := server.CommitDocuments(renter, rentalTeamCommit(rentalTeamDocument(name, violetRentalTeamBinarySize))); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("renter modified owner document: %v", err)
	}
}

func TestRentalTeamValidationAndDeletion(t *testing.T) {
	server := &violetUgcstoreServer{}
	owner := violetAuthenticatedContext("u-owner")
	name := "tenants/current/documents/users/u-local/publicItems/rental_team_4"

	if _, err := server.CommitDocuments(owner, rentalTeamCommit(rentalTeamDocument(name, violetRentalTeamBinarySize-1))); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("wrong rental size accepted: %v", err)
	}
	if _, err := server.CommitDocuments(owner, rentalTeamCommit(rentalTeamDocument(name, violetRentalTeamBinarySize))); err != nil {
		t.Fatal(err)
	}
	alias, err := server.CreateDocumentShortAlias(owner, &ugcpb.CreateDocumentShortAliasRequest{
		Parent: "tenants/current", DocumentShortAlias: &ugcpb.DocumentShortAlias{Document: name, Scope: "length6"},
	})
	if err != nil {
		t.Fatal(err)
	}
	deleteRequest := &ugcpb.CommitDocumentsRequest{Tenant: "tenants/current", WriteOperations: []*ugcpb.WriteOperation{{
		OperationType: &ugcpb.WriteOperation_DeleteDocument{DeleteDocument: &ugcpb.DeleteDocumentRequest{Name: name}},
	}}}
	if _, err := server.CommitDocuments(owner, deleteRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := server.GetDocumentShortAlias(owner, &ugcpb.GetDocumentShortAliasRequest{Name: alias.GetName()}); status.Code(err) != codes.NotFound {
		t.Fatalf("deleted rental alias remains available: %v", err)
	}
}

func TestRentalTeamAliasUsesScopedResourceName(t *testing.T) {
	server := &violetUgcstoreServer{}
	owner := violetAuthenticatedContext("u-owner")
	name := "tenants/current/documents/users/u-local/publicItems/rental_team_0"
	if _, err := server.CommitDocuments(owner, rentalTeamCommit(rentalTeamDocument(name, violetRentalTeamBinarySize))); err != nil {
		t.Fatal(err)
	}
	alias, err := server.CreateDocumentShortAlias(owner, &ugcpb.CreateDocumentShortAliasRequest{
		Parent: "tenants/current", DocumentShortAlias: &ugcpb.DocumentShortAlias{Document: name, Scope: "length6"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := nplnTenant + "/scopes/length6/documentShortAliases/"
	if !strings.HasPrefix(alias.GetName(), wantPrefix) {
		t.Fatalf("alias name = %q, want prefix %q", alias.GetName(), wantPrefix)
	}
	if _, _, ok := rentalAliasSegments(alias.GetName()); !ok {
		t.Fatalf("Violet alias parser rejected %q", alias.GetName())
	}
}

type rentalBulkTestStream struct {
	grpc.ServerStreamingServer[ugcpb.BulkGetDocumentsResponse]
	ctx  context.Context
	sent []*ugcpb.BulkGetDocumentsResponse
}

func (s *rentalBulkTestStream) Context() context.Context { return s.ctx }
func (s *rentalBulkTestStream) Send(response *ugcpb.BulkGetDocumentsResponse) error {
	s.sent = append(s.sent, response)
	return nil
}

func TestRentalTeamInitialBulkReadAfterPenaltyBinding(t *testing.T) {
	server := &violetUgcstoreServer{}
	ctx := violetAuthenticatedContext("u-bearer-subject")
	penalty := "tenants/current/documents/users/u-opaquesaveid/privateItems/penalty"
	if _, err := server.GetDocument(ctx, &ugcpb.GetDocumentRequest{Name: penalty}); err != nil {
		t.Fatal(err)
	}

	request := &ugcpb.BulkGetDocumentsRequest{Parent: "tenants/current"}
	for slot := byte('0'); slot <= '4'; slot++ {
		request.Names = append(request.Names,
			"tenants/current/documents/users/u-opaquesaveid/publicItems/rental_team_"+string(slot))
	}
	stream := &rentalBulkTestStream{ctx: ctx}
	if err := server.BulkGetDocuments(request, stream); err != nil {
		t.Fatalf("initial rental slot query failed after penalty binding: %v", err)
	}
	if len(stream.sent) != 5 {
		t.Fatalf("bulk responses = %d, want 5", len(stream.sent))
	}
	for index, response := range stream.sent {
		if response.GetMissing() != request.Names[index] {
			t.Fatalf("response %d missing = %q, want %q", index, response.GetMissing(), request.Names[index])
		}
	}
}
