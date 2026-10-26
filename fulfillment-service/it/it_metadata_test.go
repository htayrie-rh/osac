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
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metadatautil "github.com/osac-project/osac/fulfillment-service/internal/metadata"
	privatev1 "github.com/osac-project/osac/proto/gen/osac/private/v1"
	publicv1 "github.com/osac-project/osac/proto/gen/osac/public/v1"
	grpccodes "google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

const (
	metadataPrivateLabelKey               = "admin.osac.openshift.io/integration"
	metadataPrivateSubdomainKey           = "provider.admin.osac.openshift.io/integration"
	metadataPrivateAnnotationKey          = "admin.osac.openshift.io/integration"
	metadataPrivateSubdomainAnnotationKey = "controller.admin.osac.openshift.io/integration"
	metadataOrdinaryLabelKey              = "example.com/integration"
	metadataOrdinaryAnnotationKey         = "example.com/integration"
	metadataNearPrefixKey                 = "admin.osac.openshift.io.example/integration"
)

var _ = Describe("reserved metadata privacy fixtures", Label("metadata"), func() {
	It("keeps private metadata stored and hides it from tenant REST and CLI reads", func(ctx context.Context) {
		tenantAdmin := createCatalogItemTenantAdminIdentityFixture(ctx)
		tenantUser := createCatalogItemMemberIdentityFixture(ctx, tenantAdmin.TenantID)
		secondTenantUser, err := metadataIdentityForExistingUser(ctx, "charles", usersPassword, "sales")
		Expect(err).NotTo(HaveOccurred())
		accounts := metadataAccountsFromTool(tool, tenantAdmin, tenantUser, secondTenantUser)

		fixture, err := newComputeInstanceMetadataFixture(ctx, accounts)
		Expect(err).NotTo(HaveOccurred())
		before, err := fixture.privateSnapshot(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(before.GetMetadata().GetTenant()).To(Equal(accounts.tenantUser.TenantID))

		ownerReference, hasOwnerReference := before.GetMetadata().GetAnnotations()["osac.openshift.io/owner-reference"]
		By("writing reserved and ordinary metadata through the private API")
		seeded, err := fixture.updateMetadata(ctx,
			map[string]string{
				metadataPrivateLabelKey:     "stored-label",
				metadataPrivateSubdomainKey: "stored-subdomain-label",
				metadataOrdinaryLabelKey:    "visible-label",
				metadataNearPrefixKey:       "visible-lookalike-label",
			},
			map[string]string{
				metadataPrivateAnnotationKey:          "stored-annotation",
				metadataPrivateSubdomainAnnotationKey: "stored-subdomain-annotation",
				metadataOrdinaryAnnotationKey:         "visible-annotation",
				metadataNearPrefixKey:                 "visible-lookalike-annotation",
			},
		)
		Expect(err).NotTo(HaveOccurred())

		By("checking both tenant identities through the deployed REST gateway")
		for _, reader := range []struct {
			name     string
			identity metadataTestIdentity
		}{
			{name: "Tenant Admin", identity: accounts.tenantAdmin},
			{name: "Tenant User", identity: accounts.tenantUser},
		} {
			publicObject, statusCode, _, readErr := readComputeInstanceREST(ctx, reader.identity.HTTPClient, fixture.id)
			Expect(readErr).NotTo(HaveOccurred(), reader.name)
			Expect(statusCode).To(Equal(http.StatusOK), reader.name+" REST status")
			expectMetadataProjection(publicObject, seeded, accounts.tenantUser.TenantID)

			grpcClient := publicv1.NewComputeInstancesClient(reader.identity.Conn)
			grpcResponse, grpcErr := grpcClient.Get(ctx, publicv1.ComputeInstancesGetRequest_builder{Id: fixture.id}.Build())
			Expect(grpcErr).NotTo(HaveOccurred(), reader.name)
			expectMetadataProjection(grpcResponse.GetObject(), seeded, accounts.tenantUser.TenantID)
		}

		By("checking the second tenant cannot read the instance")
		_, statusCode, _, err := readComputeInstanceREST(ctx, accounts.secondTenantUser.HTTPClient, fixture.id)
		Expect(err).NotTo(HaveOccurred())
		Expect(statusCode).To(Equal(http.StatusNotFound), "second tenant REST status")
		_, err = publicv1.NewComputeInstancesClient(accounts.secondTenantUser.Conn).Get(
			ctx, publicv1.ComputeInstancesGetRequest_builder{Id: fixture.id}.Build(),
		)
		Expect(grpcstatus.Code(err)).To(Equal(grpccodes.NotFound))

		publicAdminClient := publicv1.NewComputeInstancesClient(tool.ExternalView().AdminConn())
		adminResponse, err := publicAdminClient.Get(ctx, publicv1.ComputeInstancesGetRequest_builder{Id: fixture.id}.Build())
		Expect(err).NotTo(HaveOccurred())
		expectMetadataProjection(adminResponse.GetObject(), seeded, accounts.tenantUser.TenantID)

		By("rejecting mixed tenant metadata updates without changing the private object")
		publicTenantAdmin := publicv1.NewComputeInstancesClient(accounts.tenantAdmin.Conn)
		_, err = publicTenantAdmin.Update(ctx, publicv1.ComputeInstancesUpdateRequest_builder{
			Object: publicv1.ComputeInstance_builder{
				Id: fixture.id,
				Metadata: publicv1.Metadata_builder{
					Labels: map[string]string{
						metadataPrivateLabelKey: "rejected-label",
						"example.com/rejected":  "must-not-persist",
					},
				}.Build(),
			}.Build(),
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"metadata.labels"}},
		}.Build())
		Expect(grpcstatus.Code(err)).To(Equal(grpccodes.InvalidArgument))
		afterRejectedUpdate, err := fixture.privateSnapshot(ctx)
		Expect(err).NotTo(HaveOccurred())
		expectMetadataKeyAbsent(afterRejectedUpdate.GetMetadata().GetLabels(), "example.com/rejected", "mixed Update")
		expectMetadataEntry(afterRejectedUpdate.GetMetadata().GetLabels(), metadataPrivateLabelKey, "stored-label", "mixed Update")

		By("rejecting reserved metadata even when its map is outside the update mask")
		_, err = publicTenantAdmin.Update(ctx, publicv1.ComputeInstancesUpdateRequest_builder{
			Object: publicv1.ComputeInstance_builder{
				Id: fixture.id,
				Metadata: publicv1.Metadata_builder{
					Labels:      map[string]string{"example.com/outside-mask": "must-not-persist"},
					Annotations: map[string]string{metadataPrivateAnnotationKey: "rejected-annotation"},
				}.Build(),
			}.Build(),
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"metadata.labels"}},
		}.Build())
		Expect(grpcstatus.Code(err)).To(Equal(grpccodes.InvalidArgument))
		afterOutsideMask, err := fixture.privateSnapshot(ctx)
		Expect(err).NotTo(HaveOccurred())
		expectMetadataKeyAbsent(afterOutsideMask.GetMetadata().GetLabels(), "example.com/outside-mask", "reserved input outside update mask")
		expectMetadataEntry(
			afterOutsideMask.GetMetadata().GetAnnotations(), metadataPrivateAnnotationKey, "stored-annotation", "reserved input outside update mask",
		)

		By("rejecting a reserved write through an administrator's public API identity")
		_, err = publicAdminClient.Update(ctx, publicv1.ComputeInstancesUpdateRequest_builder{
			Object: publicv1.ComputeInstance_builder{
				Id: fixture.id,
				Metadata: publicv1.Metadata_builder{
					Labels: map[string]string{metadataPrivateSubdomainKey: "rejected-admin-value"},
				}.Build(),
			}.Build(),
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"metadata.labels"}},
		}.Build())
		Expect(grpcstatus.Code(err)).To(Equal(grpccodes.InvalidArgument))

		By("updating ordinary metadata through gRPC and REST")
		grpcUpdate, err := publicTenantAdmin.Update(ctx, publicv1.ComputeInstancesUpdateRequest_builder{
			Object: publicv1.ComputeInstance_builder{
				Id: fixture.id,
				Metadata: publicv1.Metadata_builder{
					Labels: map[string]string{
						metadataOrdinaryLabelKey: "visible-from-grpc",
						metadataNearPrefixKey:    "lookalike-from-grpc",
					},
				}.Build(),
			}.Build(),
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"metadata.labels"}},
		}.Build())
		Expect(err).NotTo(HaveOccurred())
		expectMetadataEntry(grpcUpdate.GetObject().GetMetadata().GetLabels(), metadataOrdinaryLabelKey, "visible-from-grpc", "gRPC Update response")
		expectReservedMetadataHidden(grpcUpdate.GetObject())

		restAnnotations := map[string]string{
			metadataOrdinaryAnnotationKey: "visible-from-rest",
			metadataNearPrefixKey:         "lookalike-from-rest",
		}
		if hasOwnerReference {
			restAnnotations["osac.openshift.io/owner-reference"] = ownerReference
		}
		restUpdate, statusCode, _, err := updateComputeInstanceREST(
			ctx,
			accounts.tenantAdmin.HTTPClient,
			fixture.id,
			"metadata.annotations",
			publicv1.ComputeInstance_builder{
				Id: fixture.id,
				Metadata: publicv1.Metadata_builder{
					Annotations: restAnnotations,
				}.Build(),
			}.Build(),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(statusCode).To(Equal(http.StatusOK), "REST Update status")
		expectMetadataEntry(restUpdate.GetMetadata().GetAnnotations(), metadataOrdinaryAnnotationKey, "visible-from-rest", "REST Update response")
		expectReservedMetadataHidden(restUpdate)

		By("checking both tenant identities through the deployed CLI")
		for _, reader := range []struct {
			name     string
			identity metadataTestIdentity
		}{
			{name: "Tenant Admin", identity: accounts.tenantAdmin},
			{name: "Tenant User", identity: accounts.tenantUser},
		} {
			publicObject := readComputeInstanceCLI(ctx, reader.identity, fixture.id)
			Expect(publicObject.GetMetadata().GetTenant()).To(Equal(accounts.tenantUser.TenantID))
			expectMetadataEntry(publicObject.GetMetadata().GetLabels(), metadataOrdinaryLabelKey, "visible-from-grpc", "CLI read")
			expectMetadataEntry(publicObject.GetMetadata().GetLabels(), metadataNearPrefixKey, "lookalike-from-grpc", "CLI read")
			expectMetadataEntry(publicObject.GetMetadata().GetAnnotations(), metadataOrdinaryAnnotationKey, "visible-from-rest", "CLI read")
			expectMetadataEntry(publicObject.GetMetadata().GetAnnotations(), metadataNearPrefixKey, "lookalike-from-rest", "CLI read")
			expectReservedMetadataHidden(publicObject)
		}

		By("rejecting CLI reserved writes and preserving hidden entries during key removal")
		homeDir, err := tool.NewCLIHomeDir()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(os.RemoveAll(homeDir)).To(Succeed()) })
		_, stderr, exitCode := tool.LoginCLI(ctx, homeDir, accounts.tenantAdmin.Username, accounts.tenantAdmin.Password)
		Expect(exitCode).To(Equal(0), "CLI login failed: %s", stderr)
		stdout, stderr, exitCode := tool.RunCLI(ctx, homeDir,
			"label", "computeinstance", fixture.id, metadataPrivateLabelKey+"=rejected-cli-label")
		Expect(exitCode).NotTo(Equal(0))
		Expect(stdout + stderr).To(ContainSubstring("reserved for use by the system"))
		stdout, stderr, exitCode = tool.RunCLI(ctx, homeDir,
			"annotate", "computeinstance", fixture.id, metadataPrivateAnnotationKey+"=rejected-cli-annotation")
		Expect(exitCode).NotTo(Equal(0))
		Expect(stdout + stderr).To(ContainSubstring("reserved for use by the system"))
		_, stderr, exitCode = tool.RunCLI(ctx, homeDir, "label", "computeinstance", fixture.id,
			metadataPrivateLabelKey+"-", metadataPrivateSubdomainKey+"-",
			metadataOrdinaryLabelKey+"=visible-from-cli")
		Expect(exitCode).To(Equal(0), "CLI label update failed: %s", stderr)
		_, stderr, exitCode = tool.RunCLI(ctx, homeDir, "annotate", "computeinstance", fixture.id,
			metadataPrivateAnnotationKey+"-", metadataPrivateSubdomainAnnotationKey+"-",
			metadataOrdinaryAnnotationKey+"=visible-from-cli")
		Expect(exitCode).To(Equal(0), "CLI annotation update failed: %s", stderr)

		By("confirming private storage and fixture ownership metadata are unchanged")
		after, err := fixture.privateSnapshot(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.GetMetadata().GetTenant()).To(Equal(before.GetMetadata().GetTenant()))
		afterOwnerReference, afterHasOwnerReference := after.GetMetadata().GetAnnotations()["osac.openshift.io/owner-reference"]
		Expect(afterHasOwnerReference).To(Equal(hasOwnerReference))
		Expect(afterOwnerReference).To(Equal(ownerReference))
		expectedLabels := preserveReservedMetadataValues(seeded.GetMetadata().GetLabels(), map[string]string{
			metadataOrdinaryLabelKey: "visible-from-cli",
			metadataNearPrefixKey:    "lookalike-from-grpc",
		})
		expectedAnnotations := preserveReservedMetadataValues(seeded.GetMetadata().GetAnnotations(), map[string]string{
			metadataOrdinaryAnnotationKey: "visible-from-cli",
			metadataNearPrefixKey:         "lookalike-from-rest",
		})
		if hasOwnerReference {
			expectedAnnotations["osac.openshift.io/owner-reference"] = ownerReference
		}
		expectMetadataMapEqual(after.GetMetadata().GetLabels(), expectedLabels, "final private labels")
		expectMetadataMapEqual(after.GetMetadata().GetAnnotations(), expectedAnnotations, "final private annotations")
		for _, reader := range []metadataTestIdentity{accounts.tenantAdmin, accounts.tenantUser} {
			response, getErr := publicv1.NewComputeInstancesClient(reader.Conn).Get(
				ctx, publicv1.ComputeInstancesGetRequest_builder{Id: fixture.id}.Build(),
			)
			Expect(getErr).NotTo(HaveOccurred())
			expectReservedMetadataHidden(response.GetObject())
		}
	})
})

func readComputeInstanceREST(
	ctx context.Context,
	client *http.Client,
	id string,
) (*publicv1.ComputeInstance, int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"/api/fulfillment/v1/compute_instances/"+id, nil)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("build ComputeInstance REST request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("send ComputeInstance REST request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, response.StatusCode, nil, fmt.Errorf("read ComputeInstance REST response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, response.StatusCode, body, nil
	}
	object := &publicv1.ComputeInstance{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, object); err != nil {
		return nil, response.StatusCode, body, fmt.Errorf("decode ComputeInstance REST response: %w", err)
	}
	return object, response.StatusCode, body, nil
}

func preserveReservedMetadataValues(current, replacement map[string]string) map[string]string {
	result := make(map[string]string, len(current)+len(replacement))
	for key, value := range current {
		if metadatautil.IsReservedMetadataKey(key) {
			result[key] = value
		}
	}
	for key, value := range replacement {
		result[key] = value
	}
	return result
}

func expectMetadataEntry(values map[string]string, key, expected, context string) {
	GinkgoHelper()
	actual, ok := values[key]
	if !ok || actual != expected {
		Fail(fmt.Sprintf("%s: metadata entry %q was missing or had an unexpected value", context, key))
	}
}

func expectMetadataKeyAbsent(values map[string]string, key, context string) {
	GinkgoHelper()
	if _, ok := values[key]; ok {
		Fail(fmt.Sprintf("%s: metadata unexpectedly contained key %q", context, key))
	}
}

func expectMetadataMapEqual(actual, expected map[string]string, context string) {
	GinkgoHelper()
	if len(actual) != len(expected) {
		Fail(fmt.Sprintf("%s: metadata map had an unexpected number of entries", context))
	}
	for key, expectedValue := range expected {
		actualValue, ok := actual[key]
		if !ok || actualValue != expectedValue {
			Fail(fmt.Sprintf("%s: metadata entry %q was missing or had an unexpected value", context, key))
		}
	}
}

func updateComputeInstanceREST(
	ctx context.Context,
	client *http.Client,
	id string,
	updateMask string,
	object *publicv1.ComputeInstance,
) (*publicv1.ComputeInstance, int, []byte, error) {
	GinkgoHelper()
	body, err := protojson.Marshal(object)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("encode ComputeInstance REST update: %w", err)
	}
	query := url.Values{}
	query.Set("updateMask", updateMask)
	path := fmt.Sprintf("/api/fulfillment/v1/compute_instances/%s?%s", url.PathEscape(id), query.Encode())
	request, err := http.NewRequestWithContext(ctx, http.MethodPatch, path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, nil, fmt.Errorf("build ComputeInstance REST update request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("send ComputeInstance REST update request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, response.StatusCode, nil, fmt.Errorf("read ComputeInstance REST update response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, response.StatusCode, responseBody, nil
	}
	updated := &publicv1.ComputeInstance{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(responseBody, updated); err != nil {
		return nil, response.StatusCode, responseBody, fmt.Errorf("decode ComputeInstance REST update response: %w", err)
	}
	return updated, response.StatusCode, responseBody, nil
}

func readComputeInstanceCLI(ctx context.Context, identity metadataTestIdentity, id string) *publicv1.ComputeInstance {
	GinkgoHelper()
	homeDir, err := tool.NewCLIHomeDir()
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(os.RemoveAll(homeDir)).To(Succeed()) })
	_, stderr, exitCode := tool.LoginCLI(ctx, homeDir, identity.Username, identity.Password)
	Expect(exitCode).To(Equal(0), "CLI login failed: %s", stderr)
	output, stderr, exitCode := tool.RunCLI(ctx, homeDir, "get", "computeinstance", id, "-o", "json")
	Expect(exitCode).To(Equal(0), "CLI get failed: %s", stderr)
	object := &publicv1.ComputeInstance{}
	Expect((protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(output), object)).To(Succeed())
	return object
}

func expectMetadataProjection(object *publicv1.ComputeInstance, privateObject *privatev1.ComputeInstance, tenantID string) {
	Expect(object).NotTo(BeNil())
	Expect(object.GetMetadata().GetTenant()).To(Equal(tenantID))
	expectMetadataEntry(object.GetMetadata().GetLabels(), metadataOrdinaryLabelKey, "visible-label", "public projection")
	expectMetadataEntry(object.GetMetadata().GetAnnotations(), metadataOrdinaryAnnotationKey, "visible-annotation", "public projection")
	expectMetadataEntry(object.GetMetadata().GetLabels(), metadataNearPrefixKey, "visible-lookalike-label", "public projection")
	expectMetadataEntry(object.GetMetadata().GetAnnotations(), metadataNearPrefixKey, "visible-lookalike-annotation", "public projection")
	expectReservedMetadataHidden(object)
	privateOwnerReference := privateObject.GetMetadata().GetAnnotations()["osac.openshift.io/owner-reference"]
	publicOwnerReference, hasPublicOwnerReference := object.GetMetadata().GetAnnotations()["osac.openshift.io/owner-reference"]
	if privateOwnerReference == "" {
		Expect(hasPublicOwnerReference).To(BeFalse())
	} else {
		Expect(publicOwnerReference).To(Equal(privateOwnerReference))
	}
}

func expectReservedMetadataHidden(object *publicv1.ComputeInstance) {
	GinkgoHelper()
	expectMetadataKeyAbsent(object.GetMetadata().GetLabels(), metadataPrivateLabelKey, "public projection")
	expectMetadataKeyAbsent(object.GetMetadata().GetLabels(), metadataPrivateSubdomainKey, "public projection")
	expectMetadataKeyAbsent(object.GetMetadata().GetAnnotations(), metadataPrivateAnnotationKey, "public projection")
	expectMetadataKeyAbsent(object.GetMetadata().GetAnnotations(), metadataPrivateSubdomainAnnotationKey, "public projection")
}
