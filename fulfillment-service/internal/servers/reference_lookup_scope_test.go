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

package servers

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/osac-project/osac/fulfillment-service/internal/database/dao"
	"github.com/osac-project/osac/fulfillment-service/internal/references"
	privatev1 "github.com/osac-project/osac/proto/gen/osac/private/v1"
)

var _ = Describe("Reference lookup scope", func() {
	It("keeps name-only references in the owner's top-level project scope", func() {
		projects, err := dao.NewGenericDAO[*privatev1.Project]().
			SetLogger(logger).
			SetTenancyLogic(tenancy).
			Build()
		Expect(err).ToNot(HaveOccurred())
		_, err = projects.Create().SetObject(privatev1.Project_builder{
			Id: "reference-lookup-project",
			Metadata: privatev1.Metadata_builder{
				Name:   "reference-lookup-project",
				Tenant: testTenant,
			}.Build(),
		}.Build()).Do(ctx)
		Expect(err).ToNot(HaveOccurred())

		roles, err := dao.NewGenericDAO[*privatev1.Role]().
			SetLogger(logger).
			SetTenancyLogic(tenancy).
			Build()
		Expect(err).ToNot(HaveOccurred())
		created, err := roles.Create().SetObject(privatev1.Role_builder{
			Metadata: privatev1.Metadata_builder{
				Name:    "same-name-in-another-project",
				Tenant:  testTenant,
				Project: "reference-lookup-project",
			}.Build(),
		}.Build()).Do(ctx)
		Expect(err).ToNot(HaveOccurred())

		lookup := references.NewScopedDAOLookupFunc(roles)
		resolved, err := lookup(ctx, testTenant, "", "", "same-name-in-another-project")
		Expect(err).To(HaveOccurred())
		Expect(resolved).To(BeNil())

		resolved, err = lookup(ctx, testTenant, "reference-lookup-project", "", "same-name-in-another-project")
		Expect(err).ToNot(HaveOccurred())
		Expect(resolved.ID).To(Equal(created.GetObject().GetId()))

		resolved, err = lookup(ctx, "", "", created.GetObject().GetId(), "")
		Expect(err).ToNot(HaveOccurred())
		Expect(resolved.ID).To(Equal(created.GetObject().GetId()))
	})
})
