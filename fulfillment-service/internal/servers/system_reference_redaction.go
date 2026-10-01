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
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/osac-project/osac/fulfillment-service/internal/auth"
)

// redactSystemReferences removes complete system-tenant full references from a public response copy.
func redactSystemReferences(message proto.Message) {
	if message == nil || !message.ProtoReflect().IsValid() {
		return
	}
	redactSystemReferencesIn(message.ProtoReflect())
}

func redactSystemReferencesIn(message protoreflect.Message) {
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case field.IsList() && field.Message() != nil:
			list := value.List()
			if isFullReference(field.Message()) {
				kept := 0
				for i := range list.Len() {
					item := list.Get(i)
					if isSystemReference(item.Message()) {
						continue
					}
					if kept != i {
						list.Set(kept, item)
					}
					kept++
				}
				list.Truncate(kept)
			} else {
				for i := range list.Len() {
					redactSystemReferencesIn(list.Get(i).Message())
				}
			}
		case field.IsMap():
			values := value.Map()
			valueDescriptor := field.MapValue().Message()
			if valueDescriptor == nil {
				break
			}
			var remove []protoreflect.MapKey
			values.Range(func(key protoreflect.MapKey, mapValue protoreflect.Value) bool {
				if isFullReference(valueDescriptor) {
					if isSystemReference(mapValue.Message()) {
						remove = append(remove, key)
					}
				} else {
					redactSystemReferencesIn(mapValue.Message())
				}
				return true
			})
			for _, key := range remove {
				values.Clear(key)
			}
		case field.Message() != nil:
			nested := value.Message()
			if isSystemReference(nested) {
				message.Clear(field)
			} else {
				redactSystemReferencesIn(nested)
			}
		}
		return true
	})
}

func isSystemReference(message protoreflect.Message) bool {
	if !message.IsValid() || !isFullReference(message.Descriptor()) {
		return false
	}
	tenantField := message.Descriptor().Fields().ByName("tenant")
	return message.Get(tenantField).String() == auth.SystemTenant
}

func isFullReference(descriptor protoreflect.MessageDescriptor) bool {
	if !strings.HasSuffix(string(descriptor.Name()), "Reference") {
		return false
	}
	fields := descriptor.Fields()
	if fields.Len() != 4 {
		return false
	}
	for _, name := range []protoreflect.Name{"id", "name", "project", "tenant"} {
		field := fields.ByName(name)
		if field == nil || field.Kind() != protoreflect.StringKind {
			return false
		}
	}
	return true
}
