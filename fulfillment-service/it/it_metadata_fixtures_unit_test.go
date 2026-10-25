/*
Copyright (c) 2026 Red Hat Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package it

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	privatev1 "github.com/osac-project/osac/proto/gen/osac/private/v1"
	publicv1 "github.com/osac-project/osac/proto/gen/osac/public/v1"
)

func TestDiscoverMetadataMethodCapabilities(t *testing.T) {
	fullPrivate := privatev1.File_osac_private_v1_compute_instances_service_proto.Services().ByName("ComputeInstances")
	fullPublic := publicv1.File_osac_public_v1_compute_instances_service_proto.Services().ByName("ComputeInstances")
	createOnlyPrivate := privatev1.File_osac_private_v1_self_subject_access_review_service_proto.Services().ByName("SelfSubjectAccessReviews")
	createOnlyPublic := publicv1.File_osac_public_v1_self_subject_access_review_service_proto.Services().ByName("SelfSubjectAccessReviews")
	privateNATGateways := privatev1.File_osac_private_v1_nat_gateways_service_proto.Services().ByName("NATGateways")
	publicNATGateways := publicv1.File_osac_public_v1_nat_gateways_service_proto.Services().ByName("NATGateways")

	allMethods := metadataMethodCapabilities{
		PrivateCreate: true, PrivateGet: true, PrivateList: true, PrivateUpdate: true, PrivateDelete: true,
		PublicCreate: true, PublicGet: true, PublicList: true, PublicUpdate: true, PublicDelete: true,
	}
	createOnly := metadataMethodCapabilities{PrivateCreate: true, PublicCreate: true}
	privateUpdateOnly := metadataMethodCapabilities{
		PrivateCreate: true, PrivateGet: true, PrivateList: true, PrivateUpdate: true, PrivateDelete: true,
		PublicCreate: true, PublicGet: true, PublicList: true, PublicDelete: true,
	}
	noMethods := metadataMethodCapabilities{}

	for _, test := range []struct {
		name    string
		private protoreflect.ServiceDescriptor
		public  protoreflect.ServiceDescriptor
		want    metadataMethodCapabilities
	}{
		{name: "CRUD services", private: fullPrivate, public: fullPublic, want: allMethods},
		{name: "create-only services", private: createOnlyPrivate, public: createOnlyPublic, want: createOnly},
		{name: "private-only update", private: privateNATGateways, public: publicNATGateways, want: privateUpdateOnly},
		{name: "missing descriptors", want: noMethods},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := discoverMetadataMethodCapabilities(test.private, test.public); got != test.want {
				t.Fatalf("discoverMetadataMethodCapabilities() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestMetadataResourceServiceMatrix(t *testing.T) {
	if len(metadataResourceServiceNames) != 26 {
		t.Fatalf("persistent metadata service matrix contains %d resources, want 26", len(metadataResourceServiceNames))
	}
	seen := make(map[string]struct{}, len(metadataResourceServiceNames))
	for _, name := range metadataResourceServiceNames {
		if _, exists := seen[name]; exists {
			t.Errorf("metadata service %q appears more than once", name)
			continue
		}
		seen[name] = struct{}{}

		services, err := metadataServices(name)
		if err != nil {
			t.Errorf("metadataServices(%q): %v", name, err)
			continue
		}
		if !serviceHasMetadataMethod(services.private, "Get") || !serviceHasMetadataMethod(services.private, "Update") {
			t.Errorf("private %s service must expose Get and Update", name)
			continue
		}
		getObject := services.private.Methods().ByName("Get").Output().Fields().ByName("object")
		if getObject == nil || getObject.Message() == nil {
			t.Errorf("private %s Get response has no object", name)
			continue
		}
		metadata := getObject.Message().Fields().ByName("metadata")
		if metadata == nil || metadata.Message() == nil {
			t.Errorf("private %s object has no metadata field", name)
			continue
		}
		metadataDescriptor := metadata.Message()
		for _, mapName := range []protoreflect.Name{"labels", "annotations"} {
			mapField := metadataDescriptor.Fields().ByName(mapName)
			if mapField == nil || !mapField.IsMap() || mapField.MapKey().Kind() != protoreflect.StringKind || mapField.MapValue().Kind() != protoreflect.StringKind {
				t.Errorf("private %s metadata.%s is not a string map", name, mapName)
			}
		}

		updateObject := services.private.Methods().ByName("Update").Input().Fields().ByName("object")
		if updateObject == nil || updateObject.Message() == nil || updateObject.Message().FullName() != getObject.Message().FullName() {
			t.Errorf("private %s Update object does not match its Get object", name)
		}

		if name == "Hubs" || name == "StorageBackends" {
			if services.public != nil {
				t.Errorf("%s must remain private-only", name)
			}
		} else {
			if services.public == nil {
				t.Errorf("%s has no public service descriptor", name)
				continue
			}
			publicGet := services.public.Methods().ByName("Get")
			if publicGet == nil {
				t.Errorf("public %s service has no Get method", name)
				continue
			}
			publicObject := publicGet.Output().Fields().ByName("object")
			if publicObject == nil || publicObject.Message() == nil {
				t.Errorf("public %s Get response has no metadata object", name)
				continue
			}
			publicMetadata := publicObject.Message().Fields().ByName("metadata")
			if publicMetadata == nil || publicMetadata.Message() == nil {
				t.Errorf("public %s Get object has no metadata message", name)
				continue
			}
			if publicCreate := services.public.Methods().ByName("Create"); publicCreate != nil &&
				serviceHasMetadataMethod(services.private, "List") && serviceHasMetadataMethod(services.private, "Delete") {
				createObject := publicCreate.Input().Fields().ByName("object")
				if createObject == nil || createObject.Message() == nil {
					t.Errorf("public %s Create request has no object", name)
				} else if createObject.Message().FullName() != publicObject.Message().FullName() {
					t.Errorf("public %s Create object does not match its Get object", name)
				}
			}
			for _, mapName := range []protoreflect.Name{"labels", "annotations"} {
				mapField := publicMetadata.Message().Fields().ByName(mapName)
				if mapField == nil || !mapField.IsMap() || mapField.MapKey().Kind() != protoreflect.StringKind || mapField.MapValue().Kind() != protoreflect.StringKind {
					t.Errorf("public %s metadata.%s is not a string map", name, mapName)
				}
			}
			if publicUpdate := services.public.Methods().ByName("Update"); publicUpdate != nil {
				updateObject := publicUpdate.Input().Fields().ByName("object")
				if updateObject == nil || updateObject.Message() == nil {
					t.Errorf("public %s Update request has no object", name)
				} else {
					if updateObject.Message().FullName() != publicObject.Message().FullName() {
						t.Errorf("public %s Update object does not match its Get object", name)
					}
					updateMetadata := updateObject.Message().Fields().ByName("metadata")
					if updateMetadata == nil || updateMetadata.Message() == nil {
						t.Errorf("public %s Update object has no metadata field", name)
					}
				}
				mask := publicUpdate.Input().Fields().ByName("update_mask")
				if mask == nil || mask.Message() == nil {
					t.Errorf("public %s Update request has no field mask", name)
				} else {
					paths := mask.Message().Fields().ByName("paths")
					if paths == nil || !paths.IsList() || paths.Kind() != protoreflect.StringKind {
						t.Errorf("public %s Update field mask has no string paths", name)
					}
				}
			}
		}
	}
	if len(seen) != 26 {
		t.Fatalf("unique persistent metadata services = %d, want 26", len(seen))
	}
}

func TestMetadataMapValuesReplacesOnlyRequestedMap(t *testing.T) {
	metadata := privatev1.Metadata_builder{
		Labels:      map[string]string{"old-label": "old"},
		Annotations: map[string]string{"old-annotation": "old"},
	}.Build()
	if err := replaceMetadataMap(metadata.ProtoReflect(), "labels", map[string]string{"new-label": "new"}); err != nil {
		t.Fatalf("replaceMetadataMap(labels): %v", err)
	}
	object := privatev1.ComputeInstance_builder{Metadata: metadata}.Build()
	gotLabels, err := metadataMapValues(object.ProtoReflect(), "labels")
	if err != nil {
		t.Fatalf("metadataMapValues(labels): %v", err)
	}
	gotAnnotations, err := metadataMapValues(object.ProtoReflect(), "annotations")
	if err != nil {
		t.Fatalf("metadataMapValues(annotations): %v", err)
	}
	if !reflect.DeepEqual(gotLabels, map[string]string{"new-label": "new"}) {
		t.Fatalf("labels after replacement = %v, want only new-label=new", gotLabels)
	}
	if !reflect.DeepEqual(gotAnnotations, map[string]string{"old-annotation": "old"}) {
		t.Fatalf("annotations changed while replacing labels: %v", gotAnnotations)
	}
	if metadataMapsEqual(map[string]string{"left": ""}, map[string]string{"right": ""}) {
		t.Fatal("maps with different keys and empty values must not compare equal")
	}
}

func TestPublicMetadataCreateObjectSanitizesOutputFields(t *testing.T) {
	services, err := metadataServices("ComputeInstances")
	if err != nil {
		t.Fatalf("metadataServices(ComputeInstances): %v", err)
	}
	getMethod := services.public.Methods().ByName("Get")
	if getMethod == nil {
		t.Fatal("public ComputeInstances Get method is missing")
	}
	objectField := getMethod.Output().Fields().ByName("object")
	if objectField == nil || objectField.Message() == nil {
		t.Fatal("public ComputeInstances Get response has no object")
	}
	objectDescriptor := objectField.Message()
	existing := dynamicpb.NewMessage(objectDescriptor)
	objectFields := objectDescriptor.Fields()
	idField := objectFields.ByName("id")
	statusField := objectFields.ByName("status")
	metadataField := objectFields.ByName("metadata")
	if idField == nil || statusField == nil || metadataField == nil || metadataField.Message() == nil {
		t.Fatal("public ComputeInstance descriptor is missing output or metadata fields")
	}
	existing.Set(idField, protoreflect.ValueOfString("existing-id"))
	existing.Set(statusField, protoreflect.ValueOfMessage(dynamicpb.NewMessage(statusField.Message())))
	metadata := existing.Mutable(metadataField).Message()
	metadataFields := metadata.Descriptor().Fields()
	for _, fieldName := range []protoreflect.Name{"name", "tenant", "project", "creator"} {
		field := metadataFields.ByName(fieldName)
		if field == nil || field.Kind() != protoreflect.StringKind {
			t.Fatalf("metadata field %q is missing or not a string", fieldName)
		}
		metadata.Set(field, protoreflect.ValueOfString("original-"+string(fieldName)))
	}
	versionField := metadataFields.ByName("version")
	if versionField == nil || versionField.Kind() != protoreflect.Int32Kind {
		t.Fatal("metadata version field is missing or not int32")
	}
	metadata.Set(versionField, protoreflect.ValueOfInt32(7))
	for _, fieldName := range []protoreflect.Name{"creation_timestamp", "deletion_timestamp"} {
		field := metadataFields.ByName(fieldName)
		if field == nil || field.Kind() != protoreflect.MessageKind {
			t.Fatalf("metadata field %q is missing or not a message", fieldName)
		}
		metadata.Set(field, protoreflect.ValueOfMessage(dynamicpb.NewMessage(field.Message())))
	}
	if err := replaceMetadataMap(metadata, "labels", map[string]string{"old": "label"}); err != nil {
		t.Fatalf("replaceMetadataMap(labels): %v", err)
	}
	if err := replaceMetadataMap(metadata, "annotations", map[string]string{"old": "annotation"}); err != nil {
		t.Fatalf("replaceMetadataMap(annotations): %v", err)
	}

	created, err := publicMetadataCreateObject(existing, "new-name", map[string]string{"new": "label"}, map[string]string{"new": "annotation"})
	if err != nil {
		t.Fatalf("publicMetadataCreateObject(): %v", err)
	}
	if created.Has(idField) || created.Has(statusField) {
		t.Fatal("create object retained root output fields")
	}
	createdMetadata := created.Get(metadataField).Message()
	for _, fieldName := range []protoreflect.Name{"creation_timestamp", "deletion_timestamp", "creator", "version"} {
		if field := createdMetadata.Descriptor().Fields().ByName(fieldName); field == nil || createdMetadata.Has(field) {
			t.Errorf("create object retained metadata output field %q", fieldName)
		}
	}
	if got := createdMetadata.Get(metadataFields.ByName("name")).String(); got != "new-name" {
		t.Errorf("metadata.name = %q, want new-name", got)
	}
	for _, fieldName := range []protoreflect.Name{"tenant", "project"} {
		if got, want := createdMetadata.Get(metadataFields.ByName(fieldName)).String(), "original-"+string(fieldName); got != want {
			t.Errorf("metadata.%s = %q, want %q", fieldName, got, want)
		}
	}
	for fieldName, want := range map[protoreflect.Name]map[string]string{
		"labels":      {"new": "label"},
		"annotations": {"new": "annotation"},
	} {
		got, mapErr := metadataMapValues(created.ProtoReflect(), fieldName)
		if mapErr != nil {
			t.Errorf("metadataMapValues(%s): %v", fieldName, mapErr)
		} else if !metadataMapsEqual(got, want) {
			t.Errorf("metadata.%s = %v, want %v", fieldName, got, want)
		}
	}
}

func TestMetadataAccountsFromToolKeepsExistingIdentityViews(t *testing.T) {
	adminConn := &grpc.ClientConn{}
	tool := &Tool{internalView: &ToolView{adminConn: adminConn}}
	identities := []metadataTestIdentity{
		{Conn: &grpc.ClientConn{}, HTTPClient: &http.Client{}, TenantID: "tenant-admin"},
		{Conn: &grpc.ClientConn{}, HTTPClient: &http.Client{}, TenantID: "tenant-user"},
		{Conn: &grpc.ClientConn{}, HTTPClient: &http.Client{}, TenantID: "second-tenant"},
	}

	accounts := metadataAccountsFromTool(tool, identities[0], identities[1], identities[2])
	if accounts.adminPrivate != adminConn {
		t.Fatal("private administrator connection was not taken from the internal view")
	}
	for i, got := range []metadataTestIdentity{accounts.tenantAdmin, accounts.tenantUser, accounts.secondTenantUser} {
		want := identities[i]
		if got != want {
			t.Errorf("identity %d = %+v, want %+v", i, got, want)
		}
	}
}

type fakeComputeInstanceMetadataClient struct {
	object            *privatev1.ComputeInstance
	getObjects        []*privatev1.ComputeInstance
	getCalls          int
	getErr            error
	getNilResponse    bool
	getNilObject      bool
	updateErr         error
	updateErrors      []error
	updateCalls       int
	updateRequests    []*privatev1.ComputeInstancesUpdateRequest
	updateNilResponse bool
	updateNilObject   bool
	updateRequest     *privatev1.ComputeInstancesUpdateRequest
	updatedResponse   *privatev1.ComputeInstance
}

func (f *fakeComputeInstanceMetadataClient) Get(
	_ context.Context,
	_ *privatev1.ComputeInstancesGetRequest,
	_ ...grpc.CallOption,
) (*privatev1.ComputeInstancesGetResponse, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getNilResponse {
		return nil, nil
	}
	if f.getNilObject {
		return privatev1.ComputeInstancesGetResponse_builder{}.Build(), nil
	}
	object := f.object
	if f.getCalls < len(f.getObjects) {
		object = f.getObjects[f.getCalls]
	}
	f.getCalls++
	return privatev1.ComputeInstancesGetResponse_builder{
		Object: proto.Clone(object).(*privatev1.ComputeInstance),
	}.Build(), nil
}

func (f *fakeComputeInstanceMetadataClient) Update(
	_ context.Context,
	request *privatev1.ComputeInstancesUpdateRequest,
	_ ...grpc.CallOption,
) (*privatev1.ComputeInstancesUpdateResponse, error) {
	f.updateRequest = proto.Clone(request).(*privatev1.ComputeInstancesUpdateRequest)
	f.updateRequests = append(f.updateRequests, f.updateRequest)
	if f.updateCalls < len(f.updateErrors) {
		err := f.updateErrors[f.updateCalls]
		f.updateCalls++
		return nil, err
	}
	f.updateCalls++
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if f.updateNilResponse {
		return nil, nil
	}
	if f.updateNilObject {
		return privatev1.ComputeInstancesUpdateResponse_builder{}.Build(), nil
	}
	if f.updatedResponse != nil {
		return privatev1.ComputeInstancesUpdateResponse_builder{
			Object: proto.Clone(f.updatedResponse).(*privatev1.ComputeInstance),
		}.Build(), nil
	}
	f.object = proto.Clone(request.GetObject()).(*privatev1.ComputeInstance)
	return privatev1.ComputeInstancesUpdateResponse_builder{Object: proto.Clone(f.object).(*privatev1.ComputeInstance)}.Build(), nil
}

func TestComputeInstanceMetadataFixtureUpdateMetadataRefreshesAfterVersionConflict(t *testing.T) {
	stale := privatev1.ComputeInstance_builder{
		Id:       "instance-123",
		Metadata: privatev1.Metadata_builder{Version: 7, Labels: map[string]string{"example.com/existing": "kept"}}.Build(),
	}.Build()
	current := proto.Clone(stale).(*privatev1.ComputeInstance)
	current.GetMetadata().SetVersion(8)
	client := &fakeComputeInstanceMetadataClient{
		object:     current,
		getObjects: []*privatev1.ComputeInstance{stale, current},
		updateErrors: []error{
			grpcstatus.Error(codes.Aborted, "object has been modified"),
		},
	}
	fixture := &computeInstanceMetadataFixture{id: stale.GetId(), client: client}

	updated, err := fixture.updateMetadata(context.Background(), map[string]string{"example.com/new": "value"}, nil)
	if err != nil {
		t.Fatalf("updateMetadata() error = %v", err)
	}
	if client.updateCalls != 2 {
		t.Fatalf("Update calls = %d, want 2 after one version conflict", client.updateCalls)
	}
	if got := client.updateRequests[0].GetObject().GetMetadata().GetVersion(); got != 7 {
		t.Errorf("first requested version = %d, want stale version 7", got)
	}
	if got := client.updateRequests[1].GetObject().GetMetadata().GetVersion(); got != 8 {
		t.Errorf("retried requested version = %d, want refreshed version 8", got)
	}
	if got := updated.GetMetadata().GetLabels(); got["example.com/existing"] != "kept" || got["example.com/new"] != "value" {
		t.Errorf("updated labels = %v, want existing and new labels", got)
	}
}

func TestComputeInstanceMetadataFixturePrivateSnapshotReturnsCompleteCopy(t *testing.T) {
	want := privatev1.ComputeInstance_builder{
		Id: "instance-123",
		Metadata: privatev1.Metadata_builder{
			Name:    "instance",
			Tenant:  "tenant-a",
			Project: "engineering",
			Labels: map[string]string{
				"admin.osac.openshift.io/system": "private-label",
				"example.com/visible":            "ordinary-label",
			},
			Annotations: map[string]string{
				"admin.osac.openshift.io/system":    "private-annotation",
				"osac.openshift.io/owner-reference": "parent-123",
			},
		}.Build(),
		Spec: privatev1.ComputeInstanceSpec_builder{
			Template: privatev1.ComputeInstanceTemplateReference_builder{Id: "template-123"}.Build(),
		}.Build(),
		Status: privatev1.ComputeInstanceStatus_builder{
			State: privatev1.ComputeInstanceState_COMPUTE_INSTANCE_STATE_RUNNING,
		}.Build(),
	}.Build()
	client := &fakeComputeInstanceMetadataClient{object: want}
	fixture := &computeInstanceMetadataFixture{id: want.GetId(), client: client}

	got, err := fixture.privateSnapshot(context.Background())
	if err != nil {
		t.Fatalf("privateSnapshot() error = %v", err)
	}
	if !proto.Equal(got, want) {
		t.Fatalf("privateSnapshot() = %v, want full object %v", got, want)
	}
	got.GetMetadata().GetLabels()["example.com/visible"] = "changed"
	if client.object.GetMetadata().GetLabels()["example.com/visible"] != "ordinary-label" {
		t.Fatal("privateSnapshot() returned the fake client's stored object instead of an independent copy")
	}
}

func TestComputeInstanceMetadataFixturePrivateSnapshotRejectsMissingClientData(t *testing.T) {
	validObject := privatev1.ComputeInstance_builder{
		Id:       "instance-123",
		Metadata: privatev1.Metadata_builder{Tenant: "tenant-a"}.Build(),
	}.Build()
	for _, test := range []struct {
		name    string
		fixture *computeInstanceMetadataFixture
	}{
		{name: "nil fixture"},
		{name: "nil client", fixture: &computeInstanceMetadataFixture{id: "instance-123"}},
		{name: "empty ID", fixture: &computeInstanceMetadataFixture{client: &fakeComputeInstanceMetadataClient{object: validObject}}},
		{name: "nil response", fixture: &computeInstanceMetadataFixture{id: "instance-123", client: &fakeComputeInstanceMetadataClient{getNilResponse: true}}},
		{name: "nil object", fixture: &computeInstanceMetadataFixture{id: "instance-123", client: &fakeComputeInstanceMetadataClient{getNilObject: true}}},
		{name: "missing metadata", fixture: &computeInstanceMetadataFixture{id: "instance-123", client: &fakeComputeInstanceMetadataClient{object: privatev1.ComputeInstance_builder{Id: "instance-123"}.Build()}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := test.fixture.privateSnapshot(context.Background()); err == nil || got != nil {
				t.Fatalf("privateSnapshot() = %v, %v; want nil object and an error", got, err)
			}
		})
	}
}

func TestComputeInstanceMetadataFixtureUpdateMetadataMergesMapsAndUsesExactMask(t *testing.T) {
	initial := privatev1.ComputeInstance_builder{
		Id: "instance-123",
		Metadata: privatev1.Metadata_builder{
			Name:    "instance",
			Tenant:  "tenant-a",
			Version: 7,
			Labels: map[string]string{
				"admin.osac.openshift.io/existing": "private-label",
				"example.com/existing":             "ordinary-label",
			},
			Annotations: map[string]string{
				"admin.osac.openshift.io/existing":  "private-annotation",
				"osac.openshift.io/owner-reference": "parent-123",
			},
		}.Build(),
		Spec: privatev1.ComputeInstanceSpec_builder{
			Template: privatev1.ComputeInstanceTemplateReference_builder{Id: "template-123"}.Build(),
		}.Build(),
	}.Build()
	client := &fakeComputeInstanceMetadataClient{object: initial}
	fixture := &computeInstanceMetadataFixture{id: initial.GetId(), client: client}
	labels := map[string]string{
		"admin.osac.openshift.io/new": "written-label",
		"example.com/caller":          "caller-value",
	}
	annotations := map[string]string{"admin.osac.openshift.io/new": "written-annotation"}

	got, err := fixture.updateMetadata(context.Background(), labels, annotations)
	if err != nil {
		t.Fatalf("updateMetadata() error = %v", err)
	}
	if !proto.Equal(got, client.object) {
		t.Fatalf("updateMetadata() = %v, want complete updated object %v", got, client.object)
	}
	if client.updateRequest == nil {
		t.Fatal("updateMetadata() did not send an Update request")
	}
	if gotPaths := client.updateRequest.GetUpdateMask().GetPaths(); !equalStrings(gotPaths, []string{"metadata.labels", "metadata.annotations"}) {
		t.Fatalf("UpdateMask.Paths = %v, want only both metadata maps", gotPaths)
	}
	if !client.updateRequest.GetLock() {
		t.Fatal("Update request should use the version from the private snapshot")
	}
	updated := client.updateRequest.GetObject()
	if updated.GetMetadata().GetTenant() != "tenant-a" {
		t.Fatalf("updated tenant = %q, want tenant-a", updated.GetMetadata().GetTenant())
	}
	if got := updated.GetMetadata().GetAnnotations()["osac.openshift.io/owner-reference"]; got != "parent-123" {
		t.Fatalf("owner-reference = %q, want parent-123", got)
	}
	for key, value := range map[string]string{
		"admin.osac.openshift.io/existing": "private-label",
		"admin.osac.openshift.io/new":      "written-label",
		"example.com/existing":             "ordinary-label",
	} {
		if got := updated.GetMetadata().GetLabels()[key]; got != value {
			t.Errorf("label %q = %q, want %q", key, got, value)
		}
	}
	for key, value := range map[string]string{
		"admin.osac.openshift.io/existing":  "private-annotation",
		"admin.osac.openshift.io/new":       "written-annotation",
		"osac.openshift.io/owner-reference": "parent-123",
	} {
		if got := updated.GetMetadata().GetAnnotations()[key]; got != value {
			t.Errorf("annotation %q = %q, want %q", key, got, value)
		}
	}
	if len(labels) != 2 || labels["example.com/caller"] != "caller-value" {
		t.Fatalf("updateMetadata() modified the caller's labels map: %v", labels)
	}
}

func TestComputeInstanceMetadataFixtureUpdateMetadataReturnsClientErrors(t *testing.T) {
	getFailure := errors.New("private Get failed")
	updateFailure := errors.New("private Update failed")
	for _, test := range []struct {
		name           string
		client         *fakeComputeInstanceMetadataClient
		wantErr        error
		wantErrMessage string
	}{
		{name: "snapshot failure", client: &fakeComputeInstanceMetadataClient{getErr: getFailure}, wantErr: getFailure},
		{name: "update failure", client: &fakeComputeInstanceMetadataClient{object: privatev1.ComputeInstance_builder{Id: "instance-123", Metadata: &privatev1.Metadata{}}.Build(), updateErr: updateFailure}, wantErr: updateFailure},
		{name: "nil update response", client: &fakeComputeInstanceMetadataClient{object: privatev1.ComputeInstance_builder{Id: "instance-123", Metadata: &privatev1.Metadata{}}.Build(), updateNilResponse: true}, wantErrMessage: "private Update returned no ComputeInstance"},
		{name: "nil updated object", client: &fakeComputeInstanceMetadataClient{object: privatev1.ComputeInstance_builder{Id: "instance-123", Metadata: &privatev1.Metadata{}}.Build(), updateNilObject: true}, wantErrMessage: "private Update returned no ComputeInstance"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := &computeInstanceMetadataFixture{id: "instance-123", client: test.client}
			_, err := fixture.updateMetadata(context.Background(), nil, nil)
			if test.wantErrMessage != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErrMessage) {
					t.Fatalf("updateMetadata() error = %v, want error containing %q", err, test.wantErrMessage)
				}
				return
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("updateMetadata() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
