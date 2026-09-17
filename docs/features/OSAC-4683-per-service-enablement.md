# Per-Service Enablement

Per-service enablement lets a Cloud Provider Admin select which OSAC service
tiers are active during installation and later enable additional tiers with
`helm upgrade`.

## Helm configuration

The canonical values are under `global.services`. All tiers default to
`true`:

```yaml
global:
  services:
    caas:
      enabled: true
    vmaas:
      enabled: true
    bmaas:
      enabled: false
    maas:
      enabled: false
```

Helm schema validation enforces these dependencies:

- CaaS requires at least one of VMaaS or BMaaS.
- MaaS requires CaaS.

The fulfillment service validates the same combinations at startup. The
operator validates the CaaS/compute-controller dependency. When BMaaS is
disabled, the BMF operator deployment is omitted while its CRDs remain
installed so existing resources are not deleted.

## Service propagation

| Service | Fulfillment-service flag | Operator/controller behavior |
| --- | --- | --- |
| CaaS | `--enable-caas` | `OSAC_ENABLE_CLUSTER_CONTROLLER` |
| VMaaS | `--enable-vmaas` | `OSAC_ENABLE_COMPUTE_INSTANCE_CONTROLLER` |
| BMaaS | `--enable-bmaas` | `OSAC_ENABLE_BAREMETAL_INSTANCE_CONTROLLER`; BMF deployment follows `global.services.bmaas.enabled` |
| MaaS | `--enable-maas` | No MaaS controller or API service exists yet; the value is reported by Capabilities |

If no `--enable-*` flags are supplied, fulfillment-service enables all tiers
for backward compatibility.

## Runtime behavior

### Capabilities

Clients can discover enabled tiers through the unauthenticated Capabilities
endpoint:

```bash
curl --cacert <ca-bundle.pem> \
  https://<public-api-host>/api/fulfillment/v1/capabilities
```

The response contains an `enabled_services` field, for example:

```json
{
  "enabled_services": ["caas", "vmaas"]
}
```

The field is available on both public and private Capabilities APIs. Client
specific UI or CLI hiding is not provided by the fulfillment-service change.

### Disabled services

- Known-but-disabled gRPC services return `codes.Unavailable` with a
  service-specific message.
- Unknown gRPC services continue to return `codes.Unimplemented`.
- Generated REST handlers remain registered. A request to a disabled backend
  returns HTTP 503.
- `fulfillment_disabled_service_requests_total` counts rejected requests and
  currently has a `service` label.

### HostTypes

HostTypes is shared infrastructure. When HostTypes filtering is available
through OSAC-4681:

- BMaaS-disabled deployments exclude host types with network interfaces.
- VMaaS-disabled deployments exclude host types without network interfaces.
- If both compute tiers are disabled, List returns no host types and Get
  returns `codes.NotFound` for filtered host types.

Until OSAC-4681 is included in the deployed version, this filtering is not
available.

## Installation and upgrade

Install with the selected values:

```bash
helm install osac osac-installer/charts/osac -f values.yaml
```

To enable a tier later, update the values and run:

```bash
helm upgrade osac osac-installer/charts/osac -f values.yaml
```

The affected workloads restart through the normal Helm rollout. No database
migration is required; tables for all tiers already exist.

## Troubleshooting

1. Check `enabled_services` from the Capabilities endpoint.
2. Check the fulfillment-service startup entry named `Service enablement`.
3. Check `fulfillment_disabled_service_requests_total` for clients calling
   disabled tiers.
4. Check operator logs for controller enablement.
5. Confirm that BMF pods are present or absent according to
   `global.services.bmaas.enabled`.
