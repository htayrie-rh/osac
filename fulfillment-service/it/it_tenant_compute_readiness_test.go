/*
Copyright (c) 2026 Red Hat Inc.

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

	. "github.com/onsi/ginkgo/v2/dsl/core"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/osac-project/osac/fulfillment-service/internal/kubernetes/labels"
	"github.com/osac-project/osac/fulfillment-service/internal/uuid"
	osacv1alpha1 "github.com/osac-project/osac/osac-operator/api/v1alpha1"
	privatev1 "github.com/osac-project/osac/proto/gen/osac/private/v1"
)

var _ = Describe("Tenant compute readiness readback", func() {
	It("reports infrastructure transitions independently of identity readiness and other tenants", func(ctx context.Context) {
		tenants := privatev1.NewTenantsClient(tool.InternalView().AdminConn())
		projects := privatev1.NewProjectsClient(tool.InternalView().AdminConn())
		name := fmt.Sprintf("test-compute-%s", uuid.New())
		id := createTenant(ctx, tenants, name)
		DeferCleanup(func(cleanupCtx context.Context) { deleteTenant(cleanupCtx, tenants, projects, id, name) })
		otherName := fmt.Sprintf("test-other-%s", uuid.New())
		otherID := createTenant(ctx, tenants, otherName)
		DeferCleanup(func(cleanupCtx context.Context) { deleteTenant(cleanupCtx, tenants, projects, otherID, otherName) })
		waitForTenantSynced(ctx, tenants, id)
		waitForTenantSynced(ctx, tenants, otherID)

		kube := tool.KubeClient()
		key := crclient.ObjectKey{Namespace: hubNamespace, Name: name}
		Eventually(func(g Gomega) {
			object := &osacv1alpha1.Tenant{}
			g.Expect(kube.Get(ctx, key, object)).To(Succeed())
			g.Expect(object.Labels[labels.TenantUuid]).To(Equal(name))
		}, time.Minute, time.Second).Should(Succeed())

		// Snapshot the independent networking outcome before changing compute infrastructure.
		networkBaselines := map[string]*privatev1.TenantCondition{}
		for _, tenantID := range []string{id, otherID} {
			Eventually(func(g Gomega) {
				response, err := tenants.Get(ctx, privatev1.TenantsGetRequest_builder{Id: tenantID}.Build())
				g.Expect(err).NotTo(HaveOccurred())
				network := findTenantCondition(response.GetObject().GetStatus().GetConditions(), privatev1.TenantConditionType_TENANT_CONDITION_TYPE_DEFAULT_NETWORKING_READY)
				g.Expect(network).NotTo(BeNil())
				g.Expect(network.GetReason()).NotTo(BeEmpty())
				networkBaselines[tenantID] = network
			}, time.Minute, time.Second).Should(Succeed())
		}

		// A conflicting observation outside the hub namespace must not affect readiness.
		isolatedNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:        name + "-isolated",
			Annotations: map[string]string{"osac.openshift.io/tenant": name, "osac.openshift.io/owner-reference": id},
		}}
		Expect(kube.Create(ctx, isolatedNamespace)).To(Succeed())
		DeferCleanup(func(cleanupCtx context.Context) { Expect(kube.Delete(cleanupCtx, isolatedNamespace)).To(Succeed()) })
		isolatedTenant := &osacv1alpha1.Tenant{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: isolatedNamespace.Name,
			Labels:      map[string]string{labels.TenantUuid: name},
			Annotations: map[string]string{"osac.openshift.io/tenant": name, "osac.openshift.io/owner-reference": id},
		}}
		Expect(kube.Create(ctx, isolatedTenant)).To(Succeed())
		isolatedTenant.Status.Phase = osacv1alpha1.TenantPhaseFailed
		Expect(kube.Status().Update(ctx, isolatedTenant)).To(Succeed())

		expectCondition := func(tenantID string, want privatev1.ConditionStatus, reason string) {
			Eventually(func(g Gomega) {
				response, err := tenants.Get(ctx, privatev1.TenantsGetRequest_builder{Id: tenantID}.Build())
				g.Expect(err).NotTo(HaveOccurred())
				object := response.GetObject()
				g.Expect(object.GetStatus().GetState()).To(Equal(privatev1.TenantState_TENANT_STATE_SYNCED))
				compute := findTenantCondition(object.GetStatus().GetConditions(), privatev1.TenantConditionType_TENANT_CONDITION_TYPE_COMPUTE_INFRASTRUCTURE_READY)
				g.Expect(compute).NotTo(BeNil())
				g.Expect(compute.GetStatus()).To(Equal(want))
				if reason != "" {
					g.Expect(compute.GetReason()).To(Equal(reason))
				}
				vault := findTenantCondition(object.GetStatus().GetConditions(), privatev1.TenantConditionType_TENANT_CONDITION_TYPE_VAULT_READY)
				g.Expect(vault).NotTo(BeNil())
				g.Expect(vault.GetStatus()).To(Equal(privatev1.ConditionStatus_CONDITION_STATUS_TRUE))
				network := findTenantCondition(object.GetStatus().GetConditions(), privatev1.TenantConditionType_TENANT_CONDITION_TYPE_DEFAULT_NETWORKING_READY)
				g.Expect(network).NotTo(BeNil())
				baseline := networkBaselines[tenantID]
				g.Expect(network.GetStatus()).To(Equal(baseline.GetStatus()))
				g.Expect(network.GetReason()).To(Equal(baseline.GetReason()))
				g.Expect(network.GetMessage()).To(Equal(baseline.GetMessage()))
			}, 2*time.Minute, time.Second).Should(Succeed())
		}
		setPhase := func(phase osacv1alpha1.TenantPhaseType, namespaceStatus metav1.ConditionStatus) {
			Eventually(func(g Gomega) {
				object := &osacv1alpha1.Tenant{}
				g.Expect(kube.Get(ctx, key, object)).To(Succeed())
				object.Status.Phase = phase
				object.Status.Conditions = []metav1.Condition{{
					Type: string(osacv1alpha1.TenantConditionNamespaceReady), Status: namespaceStatus,
					Reason: "TestObservation", Message: "Controlled infrastructure observation", LastTransitionTime: metav1.Now(),
				}}
				g.Expect(kube.Status().Update(ctx, object)).To(Succeed())
			}, time.Minute, time.Second).Should(Succeed())
		}

		By("Observing pending infrastructure despite successful identity setup")
		setPhase(osacv1alpha1.TenantPhaseProgressing, metav1.ConditionFalse)
		expectCondition(id, privatev1.ConditionStatus_CONDITION_STATUS_FALSE, "InfrastructurePending")

		By("Observing readiness from Kubernetes without updating or signaling the API condition")
		setPhase(osacv1alpha1.TenantPhaseReady, metav1.ConditionTrue)
		expectCondition(id, privatev1.ConditionStatus_CONDITION_STATUS_TRUE, "InfrastructureReady")
		expectCondition(otherID, privatev1.ConditionStatus_CONDITION_STATUS_FALSE, "")

		By("Observing loss of readiness")
		setPhase(osacv1alpha1.TenantPhaseProgressing, metav1.ConditionFalse)
		expectCondition(id, privatev1.ConditionStatus_CONDITION_STATUS_FALSE, "InfrastructurePending")

		By("Observing infrastructure recovery")
		setPhase(osacv1alpha1.TenantPhaseReady, metav1.ConditionTrue)
		expectCondition(id, privatev1.ConditionStatus_CONDITION_STATUS_TRUE, "InfrastructureReady")

		By("Clearing readiness when the infrastructure disappears or is recreated pending")
		object := &osacv1alpha1.Tenant{}
		Expect(kube.Get(ctx, key, object)).To(Succeed())
		Expect(kube.Delete(ctx, object)).To(Succeed())
		expectCondition(id, privatev1.ConditionStatus_CONDITION_STATUS_FALSE, "")
	})
})
