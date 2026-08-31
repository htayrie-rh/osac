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
	"v1.RegisterClusterTemplatesHandler",
	"v1.RegisterClusterCatalogItemsHandler",
	"v1.RegisterClustersHandler",
	"v1.RegisterClusterVersionsHandler",
}

var vmaasHandlers = []string{
	"v1.RegisterComputeInstanceTemplatesHandler",
	"v1.RegisterComputeInstanceCatalogItemsHandler",
	"v1.RegisterComputeInstancesHandler",
	"v1.RegisterDiskImagesHandler",
	"v1.RegisterConsoleSessionsHandler",
	"v1.RegisterInstanceTypesHandler",
	"v1.RegisterVolumesHandler",
}

var bmaasHandlers = []string{
	"v1.RegisterBareMetalInstanceTemplatesHandler",
	"v1.RegisterBareMetalInstanceCatalogItemsHandler",
	"v1.RegisterBareMetalInstancesHandler",
	"v1.RegisterBareMetalInstanceTypesHandler",
}

var sharedHandlers = []string{
	"v1.RegisterCapabilitiesHandler",
	"v1.RegisterHostTypesHandler",
	"v1.RegisterVirtualNetworksHandler",
	"v1.RegisterSubnetsHandler",
	"v1.RegisterSecurityGroupsHandler",
	"v1.RegisterNATGatewaysHandler",
	"v1.RegisterExternalIPPoolsHandler",
	"v1.RegisterExternalIPsHandler",
	"v1.RegisterExternalIPAttachmentsHandler",
	"v1.RegisterRolesHandler",
	"v1.RegisterRoleBindingsHandler",
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
