from __future__ import annotations

import re
from pathlib import Path

from tests.e2e.core.metadata_inventory import (
    METADATA_RESOURCES,
    METADATA_RESOURCES_BY_NAME,
    validate_metadata_inventory,
)

EXPECTED_METADATA_TYPES = frozenset(
    {
        "AddOnOperator",
        "BareMetalInstance",
        "BareMetalInstanceCatalogItem",
        "BareMetalInstanceTemplate",
        "BareMetalInstanceType",
        "Cluster",
        "ClusterCatalogItem",
        "ClusterTemplate",
        "ClusterVersion",
        "ComputeInstance",
        "ComputeInstanceCatalogItem",
        "ComputeInstanceTemplate",
        "DiskImage",
        "ExternalIP",
        "ExternalIPAttachment",
        "ExternalIPPool",
        "FabricDomain",
        "Hub",
        "IdentityProvider",
        "InstanceType",
        "NATGateway",
        "NetworkClass",
        "Project",
        "ProjectMembership",
        "Role",
        "RoleBinding",
        "Secret",
        "SecurityGroup",
        "StorageBackend",
        "StorageTier",
        "Subnet",
        "Tenant",
        "User",
        "VirtualNetwork",
        "Volume",
    }
)


def _service_methods(api: str, service_name: str) -> frozenset[str] | None:
    api_dir = Path(__file__).resolve().parents[2] / "proto" / api / "osac" / api / "v1"
    service_pattern = re.compile(rf"^service\s+{re.escape(service_name)}\s*\{{(.*?)^\}}", re.MULTILINE | re.DOTALL)
    methods_pattern = re.compile(r"\brpc\s+(\w+)\s*\(")
    for proto_file in api_dir.glob("*_service.proto"):
        match = service_pattern.search(proto_file.read_text())
        if match:
            return frozenset(methods_pattern.findall(match.group(1)))
    return None


def test_metadata_inventory_matches_persistent_protobuf_services() -> None:
    validate_metadata_inventory()

    assert len(METADATA_RESOURCES_BY_NAME) == 35
    assert set(METADATA_RESOURCES_BY_NAME) == EXPECTED_METADATA_TYPES
    assert "SelfSubjectAccessReview" not in METADATA_RESOURCES_BY_NAME
    public_without_update = {
        resource.name for resource in METADATA_RESOURCES if resource.public and not resource.public_update
    }
    assert public_without_update == {
        "AddOnOperator",
        "BareMetalInstanceType",
        "ExternalIP",
        "ExternalIPAttachment",
        "ExternalIPPool",
        "NATGateway",
        "StorageTier",
        "Subnet",
        "VirtualNetwork",
    }

    for resource in METADATA_RESOURCES:
        assert _service_methods("private", resource.service_name) == resource.private_methods
        assert _service_methods("public", resource.service_name) == (resource.public_methods or None)
