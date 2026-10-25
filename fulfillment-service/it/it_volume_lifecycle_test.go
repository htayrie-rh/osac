/*
Copyright (c) 2025 Red Hat Inc.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the
License. You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific
language governing permissions and limitations under the License.
*/

package it

import (
	"context"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	. "github.com/onsi/ginkgo/v2/dsl/core"
	. "github.com/onsi/gomega"
	grpccodes "google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/osac-project/osac/fulfillment-service/internal/kubernetes/annotations"
	"github.com/osac-project/osac/fulfillment-service/internal/kubernetes/labels"
	"github.com/osac-project/osac/fulfillment-service/internal/uuid"
	osacv1alpha1 "github.com/osac-project/osac/osac-operator/api/v1alpha1"
	privatev1 "github.com/osac-project/osac/proto/gen/osac/private/v1"
	publicv1 "github.com/osac-project/osac/proto/gen/osac/public/v1"
)

var _ = Describe("Volume lifecycle", func() {
	var (
		ctx context.Context

		// Volumes are private-only (no public API), served on the internal
		// endpoint, which is admin-authorized. A regular user token is rejected
		// with PermissionDenied there; admin defaults to the "shared" tenant,
		// which cannot hold tenant-scoped objects. So we use the admin conn and
		// set metadata.tenant explicitly to a real, provisioned tenant.
		volumesClient         privatev1.VolumesClient
		storageBackendsClient privatev1.StorageBackendsClient
		storageTiersClient    privatev1.StorageTiersClient

		kubeClient crclient.Client

		backendId string
		tierId    string
		tierName  string
	)

	// "users" is a tenant the test tool provisions and waits for SYNCED
	// (see createTenants/usersGroup in it_tool.go). Admin has universal ("*")
	// tenant access, so it may create objects in this tenant.
	const testTenant = "users"
	const testBackendPassword = "test-password"

	BeforeEach(func() {
		ctx = context.Background()

		volumesClient = privatev1.NewVolumesClient(tool.InternalView().AdminConn())
		storageBackendsClient = privatev1.NewStorageBackendsClient(tool.InternalView().AdminConn())
		storageTiersClient = privatev1.NewStorageTiersClient(tool.InternalView().AdminConn())
		kubeClient = tool.KubeClient()

		// --- Seed a StorageBackend (global admin resource, no tenant) ---
		backendName := fmt.Sprintf("test-backend-%s", uuid.New()[24:32])
		backendResp, err := storageBackendsClient.Create(ctx,
			privatev1.StorageBackendsCreateRequest_builder{
				Object: privatev1.StorageBackend_builder{
					Metadata: privatev1.Metadata_builder{
						Name: backendName,
					}.Build(),
					Spec: privatev1.StorageBackendSpec_builder{
						Provider: "test-provider",
						Endpoint: "https://test-storage:8443",
						Credentials: privatev1.StorageBackendCredentials_builder{
							Username: "admin",
							Password: testBackendPassword,
						}.Build(),
					}.Build(),
				}.Build(),
			}.Build())
		Expect(err).ToNot(HaveOccurred())
		backendId = backendResp.GetObject().GetId()

		DeferCleanup(func() {
			_, _ = storageBackendsClient.Delete(ctx,
				privatev1.StorageBackendsDeleteRequest_builder{
					Id: backendId,
				}.Build())
		})

		// --- Seed a StorageTier with backend association ---
		// Note: the storage tiers server always sets status.state = ACTIVE on
		// create, regardless of any caller-provided state.
		tierName = fmt.Sprintf("test-tier-%s", uuid.New()[24:32])
		tierResp, err := storageTiersClient.Create(ctx,
			privatev1.StorageTiersCreateRequest_builder{
				Object: privatev1.StorageTier_builder{
					Metadata: privatev1.Metadata_builder{
						Name: tierName,
					}.Build(),
					Spec: privatev1.StorageTierSpec_builder{
						Protocol: privatev1.StorageProtocol_STORAGE_PROTOCOL_BLOCK,
						Backends: []*privatev1.BackendAssociation{
							privatev1.BackendAssociation_builder{
								BackendId: backendId,
							}.Build(),
						},
					}.Build(),
				}.Build(),
			}.Build())
		Expect(err).ToNot(HaveOccurred())
		tierId = tierResp.GetObject().GetId()

		DeferCleanup(func() {
			_, _ = storageTiersClient.Delete(ctx,
				privatev1.StorageTiersDeleteRequest_builder{
					Id: tierId,
				}.Build())
		})
	})

	// =========================================================================
	// Phase 1 — Volume Lifecycle (happy path)
	// =========================================================================

	It("should create and delete a Volume end-to-end", func() {
		projectsClient := privatev1.NewProjectsClient(tool.InternalView().AdminConn())
		project := fmt.Sprintf("test-volume-project-%s", uuid.New()[24:32])
		projectResp, err := projectsClient.Create(ctx,
			privatev1.ProjectsCreateRequest_builder{
				Object: privatev1.Project_builder{
					Metadata: privatev1.Metadata_builder{
						Name:   project,
						Tenant: testTenant,
					}.Build(),
					Spec: privatev1.ProjectSpec_builder{
						Title: "Volume integration project",
					}.Build(),
				}.Build(),
			}.Build())
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() {
			deleteProject(ctx, projectsClient, projectResp.GetObject().GetId())
		})

		volName := fmt.Sprintf("test-vol-%s", uuid.New()[24:32])

		// 1. CREATE volume (admin conn + explicit tenant)
		createResp, err := volumesClient.Create(ctx,
			privatev1.VolumesCreateRequest_builder{
				Object: privatev1.Volume_builder{
					Metadata: privatev1.Metadata_builder{
						Name:    volName,
						Tenant:  testTenant,
						Project: project,
					}.Build(),
					Spec: privatev1.VolumeSpec_builder{
						StorageTier: tierName,
						SizeGib:     10,
						AccessMode:  privatev1.VolumeAccessMode_VOLUME_ACCESS_MODE_READ_WRITE_ONCE,
					}.Build(),
				}.Build(),
			}.Build())
		Expect(err).ToNot(HaveOccurred())

		volId := createResp.GetObject().GetId()
		DeferCleanup(func() {
			_, _ = volumesClient.Delete(ctx,
				privatev1.VolumesDeleteRequest_builder{
					Id: volId,
				}.Build())
		})

		// 2. VERIFY status fields set synchronously by the tier resolver during Create.
		//    (hub is NOT set here — it is assigned asynchronously by the reconciler.)
		getResp, err := volumesClient.Get(ctx,
			privatev1.VolumesGetRequest_builder{
				Id: volId,
			}.Build())
		Expect(err).ToNot(HaveOccurred())
		vol := getResp.GetObject()
		Expect(vol.GetStatus().GetState()).To(Equal(privatev1.VolumeState_VOLUME_STATE_CREATING))
		Expect(vol.GetStatus().GetProvider()).To(Equal("test-provider"))
		Expect(vol.GetStatus().ProtoReflect().Descriptor().Fields().ByName("backend")).To(BeNil())
		Expect(vol.GetStatus().GetProtocol()).To(Equal(privatev1.StorageProtocol_STORAGE_PROTOCOL_BLOCK))
		Expect(vol.GetMetadata().GetProject()).To(Equal(project))

		// 2b. VERIFY hub is assigned asynchronously by the reconciler (selectHub → SetHub).
		Eventually(func(g Gomega) {
			opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			resp, err := volumesClient.Get(opCtx,
				privatev1.VolumesGetRequest_builder{
					Id: volId,
				}.Build())
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(resp.GetObject().GetStatus().GetHub()).ToNot(BeEmpty())
		}, time.Minute, time.Second).Should(Succeed())

		// 3. VERIFY Volume CR appears on hub (async — reconciler creates it)
		Eventually(func(g Gomega) {
			opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			crList := &osacv1alpha1.VolumeList{}
			g.Expect(kubeClient.List(opCtx, crList,
				crclient.MatchingLabels{labels.VolumeUuid: volId},
			)).To(Succeed())
			g.Expect(crList.Items).To(HaveLen(1))

			cr := crList.Items[0]
			g.Expect(cr.Spec.StorageTier).To(Equal(tierName))
			g.Expect(cr.Spec.SizeGiB).To(Equal(int64(10)))
			g.Expect(string(cr.Spec.AccessMode)).To(ContainSubstring("ReadWriteOnce"))
			g.Expect(cr.GetAnnotations()).To(HaveKeyWithValue(annotations.Tenant, testTenant))
			g.Expect(cr.GetAnnotations()).To(HaveKeyWithValue(annotations.Project, project))
		}, time.Minute, time.Second).Should(Succeed())

		// 4. FEEDBACK — simulate controller updating status to AVAILABLE
		feedbackResp, err := volumesClient.Get(ctx,
			privatev1.VolumesGetRequest_builder{
				Id: volId,
			}.Build())
		Expect(err).ToNot(HaveOccurred())

		volObj := feedbackResp.GetObject()
		volObj.GetStatus().SetState(privatev1.VolumeState_VOLUME_STATE_AVAILABLE)
		volObj.GetStatus().SetStateTransitionTime(timestamppb.New(time.Now()))
		volObj.GetStatus().SetVendorVolumeId("test-vendor-vol-123")

		_, err = volumesClient.Update(ctx,
			privatev1.VolumesUpdateRequest_builder{
				Object: volObj,
			}.Build())
		Expect(err).ToNot(HaveOccurred())

		_, err = volumesClient.Signal(ctx,
			privatev1.VolumesSignalRequest_builder{
				Id: volId,
			}.Build())
		Expect(err).ToNot(HaveOccurred())

		Eventually(func(g Gomega) {
			opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			resp, err := volumesClient.Get(opCtx,
				privatev1.VolumesGetRequest_builder{
					Id: volId,
				}.Build())
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(resp.GetObject().GetStatus().GetState()).To(
				Equal(privatev1.VolumeState_VOLUME_STATE_AVAILABLE))
			g.Expect(resp.GetObject().GetStatus().GetVendorVolumeId()).To(Equal("test-vendor-vol-123"))
		}, time.Minute, time.Second).Should(Succeed())

		// 5. DELETE volume → verify CR removed from hub
		_, err = volumesClient.Delete(ctx,
			privatev1.VolumesDeleteRequest_builder{
				Id: volId,
			}.Build())
		Expect(err).ToNot(HaveOccurred())

		Eventually(func(g Gomega) {
			opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			crList := &osacv1alpha1.VolumeList{}
			g.Expect(kubeClient.List(opCtx, crList,
				crclient.MatchingLabels{labels.VolumeUuid: volId},
			)).To(Succeed())
			g.Expect(crList.Items).To(BeEmpty())
		}, time.Minute, time.Second).Should(Succeed())
	})

	It("preserves Volume metadata through the CSI service account allowlist", func(ctx SpecContext) {
		tokenSource, err := tool.makeCSITokenSource(ctx)
		Expect(err).NotTo(HaveOccurred())
		accessToken, err := tokenSource.Token(ctx)
		Expect(err).NotTo(HaveOccurred())
		claims := jwt.MapClaims{}
		parsedToken, _, err := jwt.NewParser().ParseUnverified(accessToken.Access, claims)
		Expect(err).NotTo(HaveOccurred())
		realmAccess, ok := parsedToken.Claims.(jwt.MapClaims)["realm_access"].(map[string]any)
		Expect(ok).To(BeTrue(), "CSI service-account token must include realm_access")
		roles, ok := realmAccess["roles"].([]any)
		Expect(ok).To(BeTrue(), "CSI service-account token must include realm_access.roles")
		Expect(roles).To(ContainElement("osac-csi-driver"), "CSI token must carry its authorization role")
		csiConn, err := tool.makeGrpcConn(internalServiceAddr, tokenSource)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(csiConn.Close()).To(Succeed()) })
		csiVolumes := privatev1.NewVolumesClient(csiConn)
		adminVolumes := privatev1.NewVolumesClient(tool.InternalView().AdminConn())

		projects := privatev1.NewProjectsClient(tool.InternalView().AdminConn())
		projectName := fmt.Sprintf("test-csi-volume-project-%s", uuid.New()[24:32])
		project, err := projects.Create(ctx, privatev1.ProjectsCreateRequest_builder{
			Object: privatev1.Project_builder{
				Metadata: privatev1.Metadata_builder{Name: projectName, Tenant: testTenant}.Build(),
				Spec:     privatev1.ProjectSpec_builder{Title: "CSI metadata integration project"}.Build(),
			}.Build(),
		}.Build())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func(cleanupCtx SpecContext) {
			_, deleteErr := projects.Delete(cleanupCtx, privatev1.ProjectsDeleteRequest_builder{
				Id: project.GetObject().GetId(),
			}.Build())
			Expect(deleteErr).NotTo(HaveOccurred())
		})

		volumeName := fmt.Sprintf("test-csi-metadata-volume-%s", uuid.New()[24:32])
		created, err := csiVolumes.Create(ctx, privatev1.VolumesCreateRequest_builder{
			Object: privatev1.Volume_builder{
				Metadata: privatev1.Metadata_builder{
					Name:    volumeName,
					Tenant:  testTenant,
					Project: projectName,
					Labels: map[string]string{
						metadataPrivateLabelKey:  "csi-stored-label",
						metadataOrdinaryLabelKey: "csi-visible-label",
					},
					Annotations: map[string]string{
						metadataPrivateAnnotationKey:  "csi-stored-annotation",
						metadataOrdinaryAnnotationKey: "csi-visible-annotation",
					},
				}.Build(),
				Spec: privatev1.VolumeSpec_builder{
					StorageTier: tierName,
					SizeGib:     10,
					AccessMode:  privatev1.VolumeAccessMode_VOLUME_ACCESS_MODE_READ_WRITE_ONCE,
				}.Build(),
			}.Build(),
		}.Build())
		Expect(err).NotTo(HaveOccurred())
		volumeID := created.GetObject().GetId()
		Expect(volumeID).NotTo(BeEmpty())
		deleted := false
		DeferCleanup(func(cleanupCtx SpecContext) {
			if deleted {
				return
			}
			_, deleteErr := adminVolumes.Delete(cleanupCtx, privatev1.VolumesDeleteRequest_builder{
				Id: volumeID,
			}.Build())
			Expect(deleteErr).NotTo(HaveOccurred())
		})

		beforeResponse, err := adminVolumes.Get(ctx, privatev1.VolumesGetRequest_builder{Id: volumeID}.Build())
		Expect(err).NotTo(HaveOccurred())
		before := beforeResponse.GetObject()
		Expect(before.GetMetadata().GetLabels()).To(HaveKeyWithValue(metadataPrivateLabelKey, "csi-stored-label"))
		Expect(before.GetMetadata().GetLabels()).To(HaveKeyWithValue(metadataOrdinaryLabelKey, "csi-visible-label"))
		Expect(before.GetMetadata().GetAnnotations()).To(HaveKeyWithValue(metadataPrivateAnnotationKey, "csi-stored-annotation"))
		Expect(before.GetMetadata().GetAnnotations()).To(HaveKeyWithValue(metadataOrdinaryAnnotationKey, "csi-visible-annotation"))

		csiRead, err := csiVolumes.Get(ctx, privatev1.VolumesGetRequest_builder{Id: volumeID}.Build())
		Expect(err).NotTo(HaveOccurred())
		Expect(metadataMapsEqual(csiRead.GetObject().GetMetadata().GetLabels(), before.GetMetadata().GetLabels())).To(BeTrue())
		Expect(metadataMapsEqual(csiRead.GetObject().GetMetadata().GetAnnotations(), before.GetMetadata().GetAnnotations())).To(BeTrue())

		filter := fmt.Sprintf("this.id == %q && this.metadata.labels[%q] == %q",
			volumeID, metadataPrivateLabelKey, "csi-stored-label")
		listed, err := csiVolumes.List(ctx, privatev1.VolumesListRequest_builder{Filter: &filter}.Build())
		Expect(err).NotTo(HaveOccurred())
		Expect(listed.GetTotal()).To(Equal(int32(1)))
		Expect(listed.GetItems()).To(HaveLen(1))
		Expect(metadataMapsEqual(listed.GetItems()[0].GetMetadata().GetLabels(), before.GetMetadata().GetLabels())).To(BeTrue())
		Expect(metadataMapsEqual(listed.GetItems()[0].GetMetadata().GetAnnotations(), before.GetMetadata().GetAnnotations())).To(BeTrue())

		updateAttempt := proto.Clone(before).(*privatev1.Volume)
		updateAttempt.GetMetadata().SetLabels(map[string]string{metadataPrivateLabelKey: "must-not-change"})
		_, err = csiVolumes.Update(ctx, privatev1.VolumesUpdateRequest_builder{Object: updateAttempt}.Build())
		Expect(grpcstatus.Code(err)).To(Equal(grpccodes.PermissionDenied))
		_, err = csiVolumes.Signal(ctx, privatev1.VolumesSignalRequest_builder{Id: volumeID}.Build())
		Expect(grpcstatus.Code(err)).To(Equal(grpccodes.PermissionDenied))
		_, err = privatev1.NewHubsClient(csiConn).Create(ctx, &privatev1.HubsCreateRequest{})
		Expect(grpcstatus.Code(err)).To(Equal(grpccodes.PermissionDenied))
		_, err = privatev1.NewStorageBackendsClient(csiConn).Create(ctx, &privatev1.StorageBackendsCreateRequest{})
		Expect(grpcstatus.Code(err)).To(Equal(grpccodes.PermissionDenied))
		_, err = publicv1.NewVolumesClient(csiConn).Get(ctx, publicv1.VolumesGetRequest_builder{Id: volumeID}.Build())
		Expect(grpcstatus.Code(err)).To(Equal(grpccodes.PermissionDenied))

		afterResponse, err := adminVolumes.Get(ctx, privatev1.VolumesGetRequest_builder{Id: volumeID}.Build())
		Expect(err).NotTo(HaveOccurred())
		Expect(metadataMapsEqual(before.GetMetadata().GetLabels(), afterResponse.GetObject().GetMetadata().GetLabels())).To(BeTrue())
		Expect(metadataMapsEqual(before.GetMetadata().GetAnnotations(), afterResponse.GetObject().GetMetadata().GetAnnotations())).To(BeTrue())

		_, err = csiVolumes.Delete(ctx, privatev1.VolumesDeleteRequest_builder{Id: volumeID}.Build())
		Expect(err).NotTo(HaveOccurred())
		deleted = true
	})

	// =========================================================================
	// Phase 2 — Tier Resolver Coverage
	// =========================================================================

	Describe("Tier resolution", func() {

		It("should fail with NotFound for non-existent tier", func() {
			volName := fmt.Sprintf("test-vol-%s", uuid.New()[24:32])
			_, err := volumesClient.Create(ctx,
				privatev1.VolumesCreateRequest_builder{
					Object: privatev1.Volume_builder{
						Metadata: privatev1.Metadata_builder{
							Name:   volName,
							Tenant: testTenant,
						}.Build(),
						Spec: privatev1.VolumeSpec_builder{
							StorageTier: "tier-does-not-exist",
							SizeGib:     10,
							AccessMode:  privatev1.VolumeAccessMode_VOLUME_ACCESS_MODE_READ_WRITE_ONCE,
						}.Build(),
					}.Build(),
				}.Build())
			Expect(err).To(HaveOccurred())
			Expect(grpcstatus.Code(err)).To(Equal(grpccodes.NotFound))
		})

		// Note: two tier-resolver error branches are NOT reachable through the
		// gRPC API, so they are not covered here (they belong to unit tests):
		//   - "tier not ACTIVE" (FailedPrecondition): the storage tiers server
		//     always sets status.state = ACTIVE on create, regardless of the
		//     caller-provided state — an INACTIVE tier cannot be created.
		//   - "tier with zero backends" (FailedPrecondition): StorageTier proto
		//     validation requires spec.backends to be non-empty (InvalidArgument
		//     on create) — a backend-less tier cannot be created.

		It("should resolve the correct tier among multiple tiers", func() {
			// Seed a second backend + NFS tier
			backend2Name := fmt.Sprintf("test-backend-2-%s", uuid.New()[24:32])
			backend2Resp, err := storageBackendsClient.Create(ctx,
				privatev1.StorageBackendsCreateRequest_builder{
					Object: privatev1.StorageBackend_builder{
						Metadata: privatev1.Metadata_builder{
							Name: backend2Name,
						}.Build(),
						Spec: privatev1.StorageBackendSpec_builder{
							Provider: "test-provider-nfs",
							Endpoint: "https://test-nfs:2049",
							Credentials: privatev1.StorageBackendCredentials_builder{
								Username: "admin",
								Password: testBackendPassword,
							}.Build(),
						}.Build(),
					}.Build(),
				}.Build())
			Expect(err).ToNot(HaveOccurred())
			backend2Id := backend2Resp.GetObject().GetId()
			DeferCleanup(func() {
				_, _ = storageBackendsClient.Delete(ctx,
					privatev1.StorageBackendsDeleteRequest_builder{
						Id: backend2Id,
					}.Build())
			})

			tier2Name := fmt.Sprintf("test-tier-nfs-%s", uuid.New()[24:32])
			tier2Resp, err := storageTiersClient.Create(ctx,
				privatev1.StorageTiersCreateRequest_builder{
					Object: privatev1.StorageTier_builder{
						Metadata: privatev1.Metadata_builder{
							Name: tier2Name,
						}.Build(),
						Spec: privatev1.StorageTierSpec_builder{
							Protocol: privatev1.StorageProtocol_STORAGE_PROTOCOL_NFS,
							Backends: []*privatev1.BackendAssociation{
								privatev1.BackendAssociation_builder{
									BackendId: backend2Id,
								}.Build(),
							},
						}.Build(),
					}.Build(),
				}.Build())
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() {
				_, _ = storageTiersClient.Delete(ctx,
					privatev1.StorageTiersDeleteRequest_builder{
						Id: tier2Resp.GetObject().GetId(),
					}.Build())
			})

			// Create volume with NFS tier → should resolve to backend2 + NFS
			vol2Name := fmt.Sprintf("test-vol-%s", uuid.New()[24:32])
			vol2Resp, err := volumesClient.Create(ctx,
				privatev1.VolumesCreateRequest_builder{
					Object: privatev1.Volume_builder{
						Metadata: privatev1.Metadata_builder{
							Name:   vol2Name,
							Tenant: testTenant,
						}.Build(),
						Spec: privatev1.VolumeSpec_builder{
							StorageTier: tier2Name,
							SizeGib:     20,
							AccessMode:  privatev1.VolumeAccessMode_VOLUME_ACCESS_MODE_READ_WRITE_ONCE,
						}.Build(),
					}.Build(),
				}.Build())
			Expect(err).ToNot(HaveOccurred())
			vol2Id := vol2Resp.GetObject().GetId()
			DeferCleanup(func() {
				_, _ = volumesClient.Delete(ctx,
					privatev1.VolumesDeleteRequest_builder{
						Id: vol2Id,
					}.Build())
			})

			vol2Get, err := volumesClient.Get(ctx,
				privatev1.VolumesGetRequest_builder{Id: vol2Id}.Build())
			Expect(err).ToNot(HaveOccurred())
			Expect(vol2Get.GetObject().GetStatus().GetProvider()).To(Equal("test-provider-nfs"))
			Expect(vol2Get.GetObject().GetStatus().GetProtocol()).To(Equal(privatev1.StorageProtocol_STORAGE_PROTOCOL_NFS))

			// Create volume with original BLOCK tier → should resolve to original backend
			vol1Name := fmt.Sprintf("test-vol-%s", uuid.New()[24:32])
			vol1Resp, err := volumesClient.Create(ctx,
				privatev1.VolumesCreateRequest_builder{
					Object: privatev1.Volume_builder{
						Metadata: privatev1.Metadata_builder{
							Name:   vol1Name,
							Tenant: testTenant,
						}.Build(),
						Spec: privatev1.VolumeSpec_builder{
							StorageTier: tierName,
							SizeGib:     5,
							AccessMode:  privatev1.VolumeAccessMode_VOLUME_ACCESS_MODE_READ_WRITE_ONCE,
						}.Build(),
					}.Build(),
				}.Build())
			Expect(err).ToNot(HaveOccurred())
			vol1Id := vol1Resp.GetObject().GetId()
			DeferCleanup(func() {
				_, _ = volumesClient.Delete(ctx,
					privatev1.VolumesDeleteRequest_builder{
						Id: vol1Id,
					}.Build())
			})

			vol1Get, err := volumesClient.Get(ctx,
				privatev1.VolumesGetRequest_builder{Id: vol1Id}.Build())
			Expect(err).ToNot(HaveOccurred())
			Expect(vol1Get.GetObject().GetStatus().GetProvider()).To(Equal("test-provider"))
			Expect(vol1Get.GetObject().GetStatus().GetProtocol()).To(Equal(privatev1.StorageProtocol_STORAGE_PROTOCOL_BLOCK))
		})
	})

	// =========================================================================
	// Phase 3 — Validation Tests
	// =========================================================================

	Describe("Volume validation", func() {

		It("should reject volume without spec", func() {
			_, err := volumesClient.Create(ctx,
				privatev1.VolumesCreateRequest_builder{
					Object: privatev1.Volume_builder{
						Metadata: privatev1.Metadata_builder{
							Name:   "vol-no-spec",
							Tenant: testTenant,
						}.Build(),
					}.Build(),
				}.Build())
			Expect(err).To(HaveOccurred())
		})

		It("should reject volume with size_gib = 0", func() {
			_, err := volumesClient.Create(ctx,
				privatev1.VolumesCreateRequest_builder{
					Object: privatev1.Volume_builder{
						Metadata: privatev1.Metadata_builder{
							Name:   "vol-bad-size",
							Tenant: testTenant,
						}.Build(),
						Spec: privatev1.VolumeSpec_builder{
							StorageTier: tierName,
							SizeGib:     0,
							AccessMode:  privatev1.VolumeAccessMode_VOLUME_ACCESS_MODE_READ_WRITE_ONCE,
						}.Build(),
					}.Build(),
				}.Build())
			Expect(err).To(HaveOccurred())
		})

		It("should reject volume with negative size_gib", func() {
			_, err := volumesClient.Create(ctx,
				privatev1.VolumesCreateRequest_builder{
					Object: privatev1.Volume_builder{
						Metadata: privatev1.Metadata_builder{
							Name:   "vol-bad-size",
							Tenant: testTenant,
						}.Build(),
						Spec: privatev1.VolumeSpec_builder{
							StorageTier: tierName,
							SizeGib:     -1,
							AccessMode:  privatev1.VolumeAccessMode_VOLUME_ACCESS_MODE_READ_WRITE_ONCE,
						}.Build(),
					}.Build(),
				}.Build())
			Expect(err).To(HaveOccurred())
		})

		It("should reject volume with UNSPECIFIED access_mode", func() {
			_, err := volumesClient.Create(ctx,
				privatev1.VolumesCreateRequest_builder{
					Object: privatev1.Volume_builder{
						Metadata: privatev1.Metadata_builder{
							Name:   "vol-bad-access",
							Tenant: testTenant,
						}.Build(),
						Spec: privatev1.VolumeSpec_builder{
							StorageTier: tierName,
							SizeGib:     10,
							AccessMode:  privatev1.VolumeAccessMode_VOLUME_ACCESS_MODE_UNSPECIFIED,
						}.Build(),
					}.Build(),
				}.Build())
			Expect(err).To(HaveOccurred())
		})

		It("should reject update that changes immutable storage_tier", func() {
			// Seed a SECOND valid tier so the rejection proves immutability,
			// not just "target tier doesn't exist". Reuses backendId.
			otherTierName := fmt.Sprintf("test-tier-alt-%s", uuid.New()[24:32])
			otherTierResp, err := storageTiersClient.Create(ctx,
				privatev1.StorageTiersCreateRequest_builder{
					Object: privatev1.StorageTier_builder{
						Metadata: privatev1.Metadata_builder{Name: otherTierName}.Build(),
						Spec: privatev1.StorageTierSpec_builder{
							Protocol: privatev1.StorageProtocol_STORAGE_PROTOCOL_BLOCK,
							Backends: []*privatev1.BackendAssociation{
								privatev1.BackendAssociation_builder{
									BackendId: backendId,
								}.Build(),
							},
						}.Build(),
					}.Build(),
				}.Build())
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(func() {
				_, _ = storageTiersClient.Delete(ctx,
					privatev1.StorageTiersDeleteRequest_builder{
						Id: otherTierResp.GetObject().GetId(),
					}.Build())
			})

			volName := fmt.Sprintf("test-vol-%s", uuid.New()[24:32])
			createResp, err := volumesClient.Create(ctx,
				privatev1.VolumesCreateRequest_builder{
					Object: privatev1.Volume_builder{
						Metadata: privatev1.Metadata_builder{
							Name:   volName,
							Tenant: testTenant,
						}.Build(),
						Spec: privatev1.VolumeSpec_builder{
							StorageTier: tierName,
							SizeGib:     10,
							AccessMode:  privatev1.VolumeAccessMode_VOLUME_ACCESS_MODE_READ_WRITE_ONCE,
						}.Build(),
					}.Build(),
				}.Build())
			Expect(err).ToNot(HaveOccurred())
			volId := createResp.GetObject().GetId()
			DeferCleanup(func() {
				_, _ = volumesClient.Delete(ctx,
					privatev1.VolumesDeleteRequest_builder{
						Id: volId,
					}.Build())
			})

			getResp, err := volumesClient.Get(ctx,
				privatev1.VolumesGetRequest_builder{
					Id: volId,
				}.Build())
			Expect(err).ToNot(HaveOccurred())
			volObj := getResp.GetObject()
			volObj.GetSpec().SetStorageTier(otherTierName)
			_, err = volumesClient.Update(ctx,
				privatev1.VolumesUpdateRequest_builder{
					Object: volObj,
				}.Build())
			Expect(err).To(HaveOccurred())

			afterResp, err := volumesClient.Get(ctx,
				privatev1.VolumesGetRequest_builder{
					Id: volId,
				}.Build())
			Expect(err).ToNot(HaveOccurred())
			Expect(afterResp.GetObject().GetSpec().GetStorageTier()).To(Equal(tierName))
		})
	})
})
