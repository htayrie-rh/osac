/*
Copyright (c) 2026 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the
License. You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific
language governing permissions and limitations under the License.
*/

package grpcserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	grpccodes "google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/osac-project/osac/fulfillment-service/internal/auth"
	"github.com/osac-project/osac/fulfillment-service/internal/database"
	"github.com/osac-project/osac/fulfillment-service/internal/references"
	privatev1 "github.com/osac-project/osac/proto/gen/osac/private/v1"
)

var _ = Describe("RegisterReferenceLookups", func() {
	var validator *references.ReferenceValidator

	BeforeEach(func() {
		validator = newTestReferenceValidator()
	})

	It("registers SecretLocalReference for private and public APIs", func() {
		for _, name := range []protoreflect.FullName{
			"osac.private.v1.SecretLocalReference",
			"osac.public.v1.SecretLocalReference",
		} {
			Expect(validator.HasLookup(name)).To(BeTrue(), "missing lookup for %s", name)
		}
	})

	It("registers every reference type used in Create or Update requests", func() {
		var missing []string
		for _, name := range createOrUpdateReferenceTypes() {
			if isHandlerOwnedReferenceType(name) {
				continue
			}
			if !validator.HasLookup(name) {
				missing = append(missing, string(name))
			}
		}
		slices.Sort(missing)
		Expect(missing).To(BeEmpty(),
			"no lookup registered for Create/Update reference types:\n  %s",
			strings.Join(missing, "\n  "))
	})

	It("does not reject identity provider client_secret_secret as unregistered", func() {
		request := privatev1.IdentityProvidersCreateRequest_builder{
			Object: privatev1.IdentityProvider_builder{
				Spec: privatev1.IdentityProviderSpec_builder{
					Oidc: privatev1.OidcConfig_builder{
						ClientSecretSecret: privatev1.SecretLocalReference_builder{
							Id: "secret-id",
						}.Build(),
					}.Build(),
				}.Build(),
			}.Build(),
		}.Build()

		_, err := validator.UnaryServer(
			context.Background(),
			request,
			&grpc.UnaryServerInfo{FullMethod: "/osac.private.v1.IdentityProviders/Create"},
			func(context.Context, any) (any, error) { return "ok", nil },
		)
		if err != nil {
			st, _ := grpcstatus.FromError(err)
			Expect(st.Message()).ToNot(ContainSubstring("no lookup registered"),
				"SecretLocalReference is not registered with the interceptor: %v", err)
		}
	})

	It("hides invisible owners when validating sparse Update references", func() {
		ctrl := gomock.NewController(GinkgoT())
		tenancy := auth.NewMockTenancyLogic(ctrl)
		visibility, err := auth.NewVisibility().AddVisibleTenant("tenant-a").Build()
		Expect(err).ToNot(HaveOccurred())
		tenancy.EXPECT().DetermineVisibility(gomock.Any()).Return(visibility, nil)

		tx := database.NewMockTx(ctrl)
		tx.EXPECT().QueryRow(gomock.Any(), gomock.Any(), "binding-1").Return(ownerScopeTestRow{})
		ctx := database.TxIntoContext(context.Background(), tx)
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		validator, err := newReferenceValidator(logger, tenancy, prometheus.NewRegistry())
		Expect(err).ToNot(HaveOccurred())

		request := privatev1.RoleBindingsUpdateRequest_builder{
			Object: privatev1.RoleBinding_builder{
				Id: "binding-1",
				Spec: privatev1.RoleBindingSpec_builder{
					Role: privatev1.RoleReference_builder{Name: "role"}.Build(),
				}.Build(),
			}.Build(),
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"spec.role"}},
		}.Build()
		handlerCalled := false

		_, err = validator.UnaryServer(
			ctx,
			request,
			&grpc.UnaryServerInfo{FullMethod: privatev1.RoleBindings_Update_FullMethodName},
			func(context.Context, any) (any, error) {
				handlerCalled = true
				return "ok", nil
			},
		)

		Expect(grpcstatus.Code(err)).To(Equal(grpccodes.NotFound))
		Expect(grpcstatus.Convert(err).Message()).To(Equal("resource not found"))
		Expect(handlerCalled).To(BeFalse())
	})
})

var _ = Describe("resolveUpdateOwnerScope", func() {
	It("queries the stored owner scope when visible to the caller", func() {
		ctrl := gomock.NewController(GinkgoT())
		tx := database.NewMockTx(ctrl)
		tenancy := auth.NewMockTenancyLogic(ctrl)
		visibility, err := auth.NewVisibility().AddVisibleProject("tenant-b", "project-b").Build()
		Expect(err).ToNot(HaveOccurred())
		tenancy.EXPECT().DetermineVisibility(gomock.Any()).Return(visibility, nil)
		ctx := database.TxIntoContext(context.Background(), tx)
		tx.EXPECT().QueryRow(gomock.Any(), gomock.Any(), "binding-1").DoAndReturn(
			func(_ context.Context, query string, _ ...any) pgx.Row {
				Expect(query).To(ContainSubstring(`"role_bindings"`))
				return ownerScopeTestRow{}
			},
		)

		tenant, project, err := resolveUpdateOwnerScope(ctx, "osac.private.v1.RoleBinding", "binding-1", tenancy)

		Expect(err).ToNot(HaveOccurred())
		Expect(tenant).To(Equal("tenant-b"))
		Expect(project).To(Equal("project-b"))
	})

	It("returns the same not-found error for invisible and missing owners", func() {
		visibility, err := auth.NewVisibility().AddVisibleTenant("tenant-a").Build()
		Expect(err).ToNot(HaveOccurred())

		var messages []string
		for _, tc := range []struct {
			name string
			row  pgx.Row
		}{
			{
				name: "invisible owner",
				row:  ownerScopeTestRow{},
			},
			{
				name: "missing owner",
				row:  ownerScopeErrorTestRow{err: pgx.ErrNoRows},
			},
		} {
			By(tc.name)
			ctrl := gomock.NewController(GinkgoT())
			tx := database.NewMockTx(ctrl)
			tenancy := auth.NewMockTenancyLogic(ctrl)
			tenancy.EXPECT().DetermineVisibility(gomock.Any()).Return(visibility, nil).MaxTimes(1)
			tx.EXPECT().QueryRow(gomock.Any(), gomock.Any(), "binding-1").Return(tc.row)
			ctx := database.TxIntoContext(context.Background(), tx)

			tenant, project, err := resolveUpdateOwnerScope(ctx, "osac.private.v1.RoleBinding", "binding-1", tenancy)
			Expect(tenant).To(BeEmpty())
			Expect(project).To(BeEmpty())
			Expect(grpcstatus.Code(err)).To(Equal(grpccodes.NotFound))
			messages = append(messages, grpcstatus.Convert(err).Message())
		}

		Expect(messages).To(Equal([]string{"resource not found", "resource not found"}))
	})

	It("does not return owner scope when caller visibility cannot be determined", func() {
		ctrl := gomock.NewController(GinkgoT())
		tx := database.NewMockTx(ctrl)
		tenancy := auth.NewMockTenancyLogic(ctrl)
		tx.EXPECT().QueryRow(gomock.Any(), gomock.Any(), "binding-1").Return(ownerScopeTestRow{})
		tenancy.EXPECT().DetermineVisibility(gomock.Any()).Return(nil, errors.New("visibility unavailable"))
		ctx := database.TxIntoContext(context.Background(), tx)

		tenant, project, err := resolveUpdateOwnerScope(ctx, "osac.private.v1.RoleBinding", "binding-1", tenancy)

		Expect(err).To(MatchError(ContainSubstring("failed to determine visibility")))
		Expect(tenant).To(BeEmpty())
		Expect(project).To(BeEmpty())
	})
})

type ownerScopeTestRow struct{}

func (ownerScopeTestRow) Scan(dest ...any) error {
	*dest[0].(*string) = "tenant-b"
	*dest[1].(*string) = "project-b"
	return nil
}

type ownerScopeErrorTestRow struct {
	err error
}

func (r ownerScopeErrorTestRow) Scan(...any) error {
	return r.err
}

func isHandlerOwnedReferenceType(name protoreflect.FullName) bool {
	return name == "osac.private.v1.AddOnOperatorReference" ||
		name == "osac.public.v1.AddOnOperatorReference"
}

func newTestReferenceValidator() *references.ReferenceValidator {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tenancy, err := auth.NewGuestTenancyLogic().SetLogger(logger).Build()
	Expect(err).ToNot(HaveOccurred())
	validator, err := references.NewReferenceValidator().SetLogger(logger).Build()
	Expect(err).ToNot(HaveOccurred())
	err = registerReferenceLookups(validator, logger, tenancy, prometheus.NewRegistry())
	Expect(err).ToNot(HaveOccurred())
	return validator
}

func createOrUpdateReferenceTypes() []protoreflect.FullName {
	seen := map[protoreflect.FullName]struct{}{}
	refs := map[protoreflect.FullName]struct{}{}
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		pkg := string(fd.Package())
		if pkg != "osac.private.v1" && pkg != "osac.public.v1" {
			return true
		}
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			methods := services.Get(i).Methods()
			for j := 0; j < methods.Len(); j++ {
				method := methods.Get(j)
				name := string(method.Name())
				if name != "Create" && name != "Update" {
					continue
				}
				collectReferenceTypes(method.Input(), seen, refs)
			}
		}
		return true
	})
	result := make([]protoreflect.FullName, 0, len(refs))
	for name := range refs {
		result = append(result, name)
	}
	slices.Sort(result)
	return result
}

func collectReferenceTypes(
	md protoreflect.MessageDescriptor,
	seen map[protoreflect.FullName]struct{},
	refs map[protoreflect.FullName]struct{},
) {
	if md == nil {
		return
	}
	name := md.FullName()
	if _, ok := seen[name]; ok {
		return
	}
	seen[name] = struct{}{}

	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.Kind() != protoreflect.MessageKind || fd.IsMap() {
			continue
		}
		msg := fd.Message()
		if strings.HasSuffix(string(msg.FullName()), "Reference") {
			refs[msg.FullName()] = struct{}{}
			continue
		}
		collectReferenceTypes(msg, seen, refs)
	}
}
