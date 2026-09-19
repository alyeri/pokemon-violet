package main

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	commonpb "npln.nintendo.net/npln-practice/proto/common"
	ugcpb "npln.nintendo.net/npln-practice/proto/ugcstore/v1"
	"testing"
)

func TestPenaltyCommitRestoresCallerBindingAfterRestart(t *testing.T) {
	u := &violetUgcstoreServer{}
	ctx := violetAuthenticatedContext("u-a")
	doc := &ugcpb.Document{Name: "tenants/current/documents/users/u-opaque/privateItems/penalty", Fields: &commonpb.MapValue{Fields: map[string]*commonpb.Value{
		"disconnect_count": gamesyncIntegerValue(1),
		"penalty_start_at": gamesyncIntegerValue(0),
		"warning_flag":     {ValueType: &commonpb.Value_BooleanValue{BooleanValue: false}},
	}}}
	req := &ugcpb.CommitDocumentsRequest{Tenant: "tenants/current", WriteOperations: []*ugcpb.WriteOperation{{OperationType: &ugcpb.WriteOperation_UpdateDocument{UpdateDocument: &ugcpb.UpdateDocumentRequest{Document: doc, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"*"}}}}}}}
	if _, err := u.CommitDocuments(ctx, req); err != nil {
		t.Fatalf("complete post-restart penalty update rejected: %v", err)
	}
	got, err := u.GetDocument(ctx, &ugcpb.GetDocumentRequest{Name: doc.Name})
	if err != nil || got.GetFields().GetFields()["disconnect_count"].GetIntegerValue() != 1 {
		t.Fatalf("restored penalty binding not persisted: doc=%v err=%v", got, err)
	}
}

func TestPenaltyCommit(t *testing.T) {
	u := &violetUgcstoreServer{}
	a, b := violetAuthenticatedContext("u-a"), violetAuthenticatedContext("u-b")
	name := "tenants/current/documents/users/u-opaque/privateItems/penalty"
	doc, err := u.GetDocument(a, &ugcpb.GetDocumentRequest{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = u.GetDocument(b, &ugcpb.GetDocumentRequest{Name: name}); err != nil {
		t.Fatal(err)
	}
	doc.Fields.Fields["disconnect_count"] = gamesyncIntegerValue(3)
	req := &ugcpb.CommitDocumentsRequest{Tenant: "tenants/current", WriteOperations: []*ugcpb.WriteOperation{{OperationType: &ugcpb.WriteOperation_UpdateDocument{UpdateDocument: &ugcpb.UpdateDocumentRequest{Document: doc, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"*"}}}}}}}
	result, err := u.CommitDocuments(a, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.WriteResults) != 1 || result.CommitTime == nil {
		t.Fatal("missing commit result")
	}
	got, _ := u.GetDocument(a, &ugcpb.GetDocumentRequest{Name: name})
	if got.Fields.Fields["disconnect_count"].GetIntegerValue() != 3 {
		t.Fatal("update lost")
	}
	other, _ := u.GetDocument(b, &ugcpb.GetDocumentRequest{Name: name})
	if other.Fields.Fields["disconnect_count"].GetIntegerValue() != 0 {
		t.Fatal("cross-user write")
	}
	if _, err = u.CommitDocuments(context.Background(), req); status.Code(err) != codes.Unauthenticated {
		t.Fatal("unauthenticated write")
	}
	doc.Name = "tenants/current/documents/users/u-other/privateItems/penalty"
	if _, err = u.CommitDocuments(a, req); status.Code(err) != codes.PermissionDenied {
		t.Fatal("foreign alias accepted")
	}
	doc.Name = name
	doc.Fields.Fields["disconnect_count"] = gamesyncIntegerValue(-1)
	if _, err = u.CommitDocuments(a, req); status.Code(err) != codes.InvalidArgument {
		t.Fatal("negative count accepted")
	}
	got, _ = u.GetDocument(a, &ugcpb.GetDocumentRequest{Name: name})
	if got.Fields.Fields["disconnect_count"].GetIntegerValue() != 3 {
		t.Fatal("failed write changed state")
	}
	doc.Fields.Fields["disconnect_count"] = gamesyncStringValue("bad")
	if _, err = u.CommitDocuments(a, req); status.Code(err) != codes.InvalidArgument {
		t.Fatal("wrong type accepted")
	}
}
