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

package dao

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	privatev1 "github.com/osac-project/osac/proto/gen/osac/private/v1"
	publicv1 "github.com/osac-project/osac/proto/gen/osac/public/v1"
)

func TestFilterTranslatorReservedMetadataKeys(t *testing.T) {
	publicTranslator := newMetadataFilterTranslator(t, (*publicv1.ComputeInstance)(nil).ProtoReflect().Descriptor())
	privateTranslator := newMetadataFilterTranslator(t, (*privatev1.ComputeInstance)(nil).ProtoReflect().Descriptor())

	reservedFilters := []string{
		`this.metadata.labels['admin.osac.openshift.io/owner-reference'] == 'stored-value'`,
		`this.metadata.labels['operator.admin.osac.openshift.io/provider-id'] != ''`,
		`'admin.osac.openshift.io/owner-reference' in this.metadata.labels`,
	}
	for _, filter := range reservedFilters {
		t.Run(filter, func(t *testing.T) {
			if _, err := publicTranslator.Translate(context.Background(), filter); err == nil {
				t.Fatal("public filter translation accepted a reserved metadata key")
			} else if strings.Contains(err.Error(), "owner-reference") || strings.Contains(err.Error(), "provider-id") {
				t.Fatal("filter error exposed the reserved metadata key")
			}
			if _, err := privateTranslator.Translate(context.Background(), filter); err != nil {
				t.Fatalf("private filter translation rejected reserved metadata: %v", err)
			}
		})
	}

	if _, err := publicTranslator.Translate(context.Background(), `this.metadata.labels['example.com/app'] == 'web'`); err != nil {
		t.Fatalf("public filter translation rejected an ordinary metadata key: %v", err)
	}
}

func newMetadataFilterTranslator(t *testing.T, descriptor protoreflect.MessageDescriptor) *FilterTranslator {
	t.Helper()
	translator, err := NewFilterTranslator().SetLogger(slog.Default()).SetDescriptor(descriptor).Build()
	if err != nil {
		t.Fatalf("build metadata filter translator: %v", err)
	}
	return translator
}
