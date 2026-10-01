/*
Copyright (c) 2026 Red Hat Inc.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the License.
You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific
language governing permissions and limitations under the License.
*/

package servers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/osac-project/osac/fulfillment-service/internal/auth"
	privatev1 "github.com/osac-project/osac/proto/gen/osac/private/v1"
	publicv1 "github.com/osac-project/osac/proto/gen/osac/public/v1"
	testsv1 "github.com/osac-project/osac/proto/gen/osac/tests/v1"
)

var _ = Describe("system reference redaction", func() {
	It("removes system references recursively and keeps other messages and tenants", func() {
		ref := func(tenant, id string) *testsv1.TestTargetReference {
			return testsv1.TestTargetReference_builder{Tenant: tenant, Id: id, Name: id}.Build()
		}
		message := testsv1.TestRefContainers_builder{
			Target:  ref(auth.SystemTenant, "singular"),
			Targets: []*testsv1.TestTargetReference{ref(auth.SystemTenant, "list-secret"), ref("tenant-a", "list-visible")},
			TargetsByName: map[string]*testsv1.TestTargetReference{
				"secret":  ref(auth.SystemTenant, "map-secret"),
				"visible": ref("tenant-a", "map-visible"),
			},
			Nested:    testsv1.TestRefNestedContainer_builder{Target: ref(auth.SystemTenant, "nested")}.Build(),
			Selection: testsv1.TestRefSelection_builder{Reference: ref(auth.SystemTenant, "oneof")}.Build(),
			Metadata: testsv1.Metadata_builder{
				Tenant: auth.SystemTenant,
				Labels: map[string]string{"preserved": "value"},
			}.Build(),
			LocalTarget: testsv1.TestTargetLocalReference_builder{
				Id:   "local",
				Name: "local",
			}.Build(),
		}.Build()

		redactSystemReferences(message)

		Expect(message.GetTarget()).To(BeNil())
		Expect(message.GetTargets()).To(HaveLen(1))
		Expect(message.GetTargets()[0].GetTenant()).To(Equal("tenant-a"))
		Expect(message.GetTargets()[0].GetId()).To(Equal("list-visible"))
		Expect(message.GetTargetsByName()).To(HaveKey("visible"))
		Expect(message.GetTargetsByName()).NotTo(HaveKey("secret"))
		Expect(message.GetNested().GetTarget()).To(BeNil())
		Expect(message.GetSelection().GetReference()).To(BeNil())
		Expect(message.GetMetadata().GetTenant()).To(Equal(auth.SystemTenant))
		Expect(message.GetMetadata().GetLabels()).To(HaveKeyWithValue("preserved", "value"))
		Expect(message.GetLocalTarget().GetId()).To(Equal("local"))
	})

	It("redacts only a copied private-to-public API response", func() {
		privateCluster := privatev1.Cluster_builder{
			Metadata: privatev1.Metadata_builder{Tenant: auth.SystemTenant}.Build(),
			Spec: privatev1.ClusterSpec_builder{AddOnOperators: []*privatev1.AddOnOperatorReference{
				privatev1.AddOnOperatorReference_builder{Tenant: auth.SystemTenant, Id: "private"}.Build(),
				privatev1.AddOnOperatorReference_builder{Tenant: "tenant-a", Id: "visible"}.Build(),
			}}.Build(),
		}.Build()
		publicCluster := &publicv1.Cluster{}
		mapper, err := NewGenericMapper[*privatev1.Cluster, *publicv1.Cluster]().SetLogger(logger).Build()
		Expect(err).NotTo(HaveOccurred())

		Expect(mapper.Copy(ctx, privateCluster, publicCluster)).To(Succeed())

		Expect(publicCluster.GetSpec().GetAddOnOperators()).To(HaveLen(1))
		Expect(publicCluster.GetSpec().GetAddOnOperators()[0].GetTenant()).To(Equal("tenant-a"))
		Expect(publicCluster.GetMetadata().GetTenant()).To(Equal(auth.SystemTenant))
		Expect(privateCluster.GetSpec().GetAddOnOperators()).To(HaveLen(2))
		Expect(privateCluster.GetSpec().GetAddOnOperators()[0].GetTenant()).To(Equal(auth.SystemTenant))

		privateResponse := &privatev1.Cluster{}
		privateMapper, err := NewGenericMapper[*privatev1.Cluster, *privatev1.Cluster]().SetLogger(logger).Build()
		Expect(err).NotTo(HaveOccurred())
		Expect(privateMapper.Copy(ctx, privateCluster, privateResponse)).To(Succeed())
		Expect(privateResponse.GetSpec().GetAddOnOperators()).To(HaveLen(2))
		Expect(privateResponse.GetSpec().GetAddOnOperators()[0].GetTenant()).To(Equal(auth.SystemTenant))
	})
})
