/*
Copyright (c) 2026 Red Hat Inc.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the
License. You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific
language governing permissions and limitations under the License.
*/

package restgateway

import (
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/osac-project/osac/fulfillment-service/internal/services"
)

// handlerName extracts the short function name from a handlerRegistrar function pointer.
func handlerName(h handlerRegistrar) string {
	name := runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		if prev := strings.LastIndex(name[:idx], "/"); prev >= 0 {
			idx = prev
		}
		name = name[idx+1:]
	}
	return name
}

func handlerNames(handlers []handlerRegistrar) []string {
	names := make([]string, len(handlers))
	for i, h := range handlers {
		names[i] = handlerName(h)
	}
	return names
}

func containsAll(names []string, expected []string) []string {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	var missing []string
	for _, e := range expected {
		if !set[e] {
			missing = append(missing, e)
		}
	}
	return missing
}

func containsNone(names []string, excluded []string) []string {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	var found []string
	for _, e := range excluded {
		if set[e] {
			found = append(found, e)
		}
	}
	return found
}

var caasHandlers = []string{
	"public/v1.RegisterClusterTemplatesHandler",
	"public/v1.RegisterClusterCatalogItemsHandler",
	"public/v1.RegisterClustersHandler",
	"public/v1.RegisterClusterVersionsHandler",
	"private/v1.RegisterClusterTemplatesHandler",
	"private/v1.RegisterClusterCatalogItemsHandler",
	"private/v1.RegisterClustersHandler",
	"private/v1.RegisterClusterVersionsHandler",
}

var vmaasHandlers = []string{
	"public/v1.RegisterComputeInstanceTemplatesHandler",
	"public/v1.RegisterComputeInstanceCatalogItemsHandler",
	"public/v1.RegisterComputeInstancesHandler",
	"public/v1.RegisterDiskImagesHandler",
	"public/v1.RegisterConsoleSessionsHandler",
	"public/v1.RegisterInstanceTypesHandler",
	"private/v1.RegisterComputeInstanceTemplatesHandler",
	"private/v1.RegisterComputeInstanceCatalogItemsHandler",
	"private/v1.RegisterComputeInstancesHandler",
	"private/v1.RegisterDiskImagesHandler",
	"private/v1.RegisterInstanceTypesHandler",
	"private/v1.RegisterVolumesHandler",
}

var bmaasHandlers = []string{
	"public/v1.RegisterBareMetalInstanceTemplatesHandler",
	"public/v1.RegisterBareMetalInstanceCatalogItemsHandler",
	"public/v1.RegisterBareMetalInstancesHandler",
	"public/v1.RegisterBareMetalInstanceTypesHandler",
	"private/v1.RegisterBareMetalInstanceTemplatesHandler",
	"private/v1.RegisterBareMetalInstanceCatalogItemsHandler",
	"private/v1.RegisterBareMetalInstancesHandler",
	"private/v1.RegisterBareMetalInstanceTypesHandler",
}

var sharedHandlers = []string{
	"public/v1.RegisterCapabilitiesHandler",
	"public/v1.RegisterHostTypesHandler",
	"public/v1.RegisterVirtualNetworksHandler",
	"public/v1.RegisterSubnetsHandler",
	"public/v1.RegisterSecurityGroupsHandler",
	"public/v1.RegisterNATGatewaysHandler",
	"public/v1.RegisterExternalIPPoolsHandler",
	"public/v1.RegisterExternalIPsHandler",
	"public/v1.RegisterExternalIPAttachmentsHandler",
	"public/v1.RegisterRolesHandler",
	"public/v1.RegisterRoleBindingsHandler",
	"private/v1.RegisterCapabilitiesHandler",
	"private/v1.RegisterHostTypesHandler",
	"private/v1.RegisterVirtualNetworksHandler",
	"private/v1.RegisterSubnetsHandler",
	"private/v1.RegisterSecurityGroupsHandler",
	"private/v1.RegisterNATGatewaysHandler",
	"private/v1.RegisterExternalIPPoolsHandler",
	"private/v1.RegisterExternalIPsHandler",
	"private/v1.RegisterExternalIPAttachmentsHandler",
	"private/v1.RegisterRolesHandler",
	"private/v1.RegisterRoleBindingsHandler",
}

func TestBuildHandlerList_AllEnabled(t *testing.T) {
	handlers := buildHandlerList(&services.Flags{CaaS: true, VMaaS: true, BMaaS: true, MaaS: true})
	names := handlerNames(handlers)

	for _, group := range []struct {
		label    string
		expected []string
	}{
		{"CaaS", caasHandlers},
		{"VMaaS", vmaasHandlers},
		{"BMaaS", bmaasHandlers},
		{"shared", sharedHandlers},
	} {
		if missing := containsAll(names, group.expected); len(missing) > 0 {
			t.Errorf("all-enabled: missing %s handlers: %v", group.label, missing)
		}
	}
}

func TestBuildHandlerList_BMaaSDisabled(t *testing.T) {
	handlers := buildHandlerList(&services.Flags{CaaS: true, VMaaS: true, BMaaS: false, MaaS: false})
	names := handlerNames(handlers)

	if found := containsNone(names, bmaasHandlers); len(found) > 0 {
		t.Errorf("BMaaS disabled: found BMaaS handlers that should be absent: %v", found)
	}
	if missing := containsAll(names, caasHandlers); len(missing) > 0 {
		t.Errorf("BMaaS disabled: missing CaaS handlers: %v", missing)
	}
	if missing := containsAll(names, vmaasHandlers); len(missing) > 0 {
		t.Errorf("BMaaS disabled: missing VMaaS handlers: %v", missing)
	}
}

func TestBuildHandlerList_OnlyVMaaS(t *testing.T) {
	handlers := buildHandlerList(&services.Flags{CaaS: false, VMaaS: true, BMaaS: false, MaaS: false})
	names := handlerNames(handlers)

	if found := containsNone(names, caasHandlers); len(found) > 0 {
		t.Errorf("only VMaaS: found CaaS handlers that should be absent: %v", found)
	}
	if found := containsNone(names, bmaasHandlers); len(found) > 0 {
		t.Errorf("only VMaaS: found BMaaS handlers that should be absent: %v", found)
	}
	if missing := containsAll(names, vmaasHandlers); len(missing) > 0 {
		t.Errorf("only VMaaS: missing VMaaS handlers: %v", missing)
	}
}

func TestBuildHandlerList_SharedAlwaysPresent(t *testing.T) {
	handlers := buildHandlerList(&services.Flags{CaaS: false, VMaaS: false, BMaaS: false, MaaS: false})
	names := handlerNames(handlers)

	if missing := containsAll(names, sharedHandlers); len(missing) > 0 {
		t.Errorf("all disabled: missing shared handlers: %v", missing)
	}
}
