package currentauthority

// This finite contract comes from the reviewed migration definitions.
// Requalify changed functions and affected namespace operations before updating it.
const fleetSchemaContractJSON = `{
  "checks": {
    "loomplane_fleet_namespace_attempts": {
      "fleet_namespace_attempt_digest": "CHECK ((input_digest ~ '^[a-f0-9]{64}$'::text))",
      "fleet_namespace_attempt_generation": "CHECK (((generation > 0) AND (ownership_epoch > 0)))",
      "fleet_namespace_attempt_identity": "CHECK (((id <> ''::text) AND (org_id <> ''::text) AND (operation_id <> ''::text) AND (project_id <> ''::text) AND (fleet_id <> ''::text) AND (instance_id <> ''::text) AND (environment_id <> ''::text) AND (key_id <> ''::text) AND (authority_id <> ''::text)))",
      "fleet_namespace_attempt_kind": "CHECK ((kind = ANY (ARRAY['reserve'::text, 'checkpoint'::text, 'renew'::text, 'publish'::text])))",
      "fleet_namespace_attempt_payload": "CHECK (((jsonb_typeof(input) = 'object'::text) AND (jsonb_typeof(result) = 'object'::text) AND (octet_length((input)::text) <= 2097152) AND (octet_length((result)::text) <= 2097152)))",
      "fleet_namespace_attempt_revision": "CHECK ((((kind = 'reserve'::text) AND (expected_namespace_revision = 0) AND (result_namespace_revision = 1) AND (expected_operation_revision = 0) AND (result_operation_revision = 1)) OR ((kind = ANY (ARRAY['checkpoint'::text, 'publish'::text])) AND (expected_namespace_revision > 0) AND (result_namespace_revision = (expected_namespace_revision + 1)) AND (expected_operation_revision > 0) AND (result_operation_revision = (expected_operation_revision + 1))) OR ((kind = 'renew'::text) AND (expected_namespace_revision > 0) AND (result_namespace_revision = (expected_namespace_revision + 1)) AND (expected_operation_revision > 0) AND (result_operation_revision = expected_operation_revision))))",
      "fleet_namespace_attempt_time": "CHECK ((isfinite(authorized_at) AND (authorized_at_ns >= 0) AND (authorized_at_ns <= 999)))"
    },
    "loomplane_fleet_namespaces": {
      "fleet_namespace_revision": "CHECK ((revision > 0))"
    }
  },
  "columns": {
    "loomplane_cloud_connections": {
      "account": {
        "required": true,
        "type": "text"
      },
      "checked_at": {
        "required": false,
        "type": "timestamp with time zone"
      },
      "cloud": {
        "required": true,
        "type": "text"
      },
      "created_at": {
        "required": true,
        "type": "timestamp with time zone"
      },
      "created_by": {
        "required": true,
        "type": "text"
      },
      "details": {
        "required": true,
        "type": "jsonb"
      },
      "generation": {
        "required": true,
        "type": "bigint"
      },
      "id": {
        "required": true,
        "type": "text"
      },
      "message": {
        "required": true,
        "type": "text"
      },
      "method": {
        "required": true,
        "type": "text"
      },
      "missing": {
        "required": true,
        "type": "jsonb"
      },
      "name": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "signed_in_by": {
        "required": true,
        "type": "text"
      },
      "status": {
        "required": true,
        "type": "text"
      },
      "updated_at": {
        "required": true,
        "type": "timestamp with time zone"
      }
    },
    "loomplane_cloud_profiles": {
      "created_at": {
        "required": true,
        "type": "timestamp with time zone"
      },
      "id": {
        "required": true,
        "type": "text"
      },
      "name": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "platform_owned": {
        "required": true,
        "type": "boolean"
      },
      "project_id": {
        "required": true,
        "type": "text"
      },
      "revision": {
        "required": true,
        "type": "bigint"
      },
      "updated_at": {
        "required": true,
        "type": "timestamp with time zone"
      }
    },
    "loomplane_cloud_project_grants": {
      "cloud_id": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "project_id": {
        "required": true,
        "type": "text"
      },
      "revision": {
        "required": true,
        "type": "bigint"
      },
      "revoked": {
        "required": true,
        "type": "boolean"
      },
      "target_ids": {
        "required": true,
        "type": "text[]"
      },
      "updated_at": {
        "required": true,
        "type": "timestamp with time zone"
      }
    },
    "loomplane_cloud_targets": {
      "cloud_id": {
        "required": true,
        "type": "text"
      },
      "connection_id": {
        "required": false,
        "type": "text"
      },
      "created_at": {
        "required": true,
        "type": "timestamp with time zone"
      },
      "display_name": {
        "required": true,
        "type": "text"
      },
      "id": {
        "required": true,
        "type": "text"
      },
      "location_id": {
        "required": false,
        "type": "text"
      },
      "managed_resource_id": {
        "required": false,
        "type": "text"
      },
      "name": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "pool_id": {
        "required": false,
        "type": "text"
      },
      "project_id": {
        "required": true,
        "type": "text"
      },
      "provider": {
        "required": true,
        "type": "text"
      },
      "region": {
        "required": true,
        "type": "text"
      },
      "revision": {
        "required": true,
        "type": "bigint"
      },
      "runtime": {
        "required": true,
        "type": "text"
      },
      "tags": {
        "required": true,
        "type": "text[]"
      },
      "updated_at": {
        "required": true,
        "type": "timestamp with time zone"
      }
    },
    "loomplane_cluster_capacity_inventory": {
      "org_id": {
        "required": true,
        "type": "text"
      },
      "resource_id": {
        "required": true,
        "type": "text"
      },
      "schema_version": {
        "required": true,
        "type": "integer"
      }
    },
    "loomplane_cluster_demand_cohorts": {
      "org_id": {
        "required": true,
        "type": "text"
      },
      "resource_id": {
        "required": true,
        "type": "text"
      },
      "schema_version": {
        "required": true,
        "type": "integer"
      }
    },
    "loomplane_cluster_operation_steps": {
      "body": {
        "required": true,
        "type": "jsonb"
      },
      "name": {
        "required": true,
        "type": "text"
      },
      "operation_id": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "request_digest": {
        "required": true,
        "type": "text"
      },
      "state": {
        "required": true,
        "type": "text"
      }
    },
    "loomplane_cluster_operations": {
      "body": {
        "required": true,
        "type": "jsonb"
      },
      "cloud_id": {
        "required": true,
        "type": "text"
      },
      "digest": {
        "required": true,
        "type": "text"
      },
      "fencing_epoch": {
        "required": true,
        "type": "bigint"
      },
      "id": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "principal": {
        "required": true,
        "type": "jsonb"
      },
      "resource_id": {
        "required": false,
        "type": "text"
      },
      "revision": {
        "required": true,
        "type": "bigint"
      },
      "status": {
        "required": true,
        "type": "text"
      },
      "updated_at": {
        "required": true,
        "type": "timestamp with time zone"
      }
    },
    "loomplane_cluster_pool_advice": {
      "org_id": {
        "required": true,
        "type": "text"
      },
      "resource_id": {
        "required": true,
        "type": "text"
      },
      "schema_version": {
        "required": true,
        "type": "integer"
      }
    },
    "loomplane_cluster_pool_growth": {
      "org_id": {
        "required": true,
        "type": "text"
      },
      "resource_id": {
        "required": true,
        "type": "text"
      },
      "schema_version": {
        "required": true,
        "type": "integer"
      }
    },
    "loomplane_cluster_workload_observed_accounting": {
      "org_id": {
        "required": true,
        "type": "text"
      },
      "resource_id": {
        "required": true,
        "type": "text"
      },
      "schema_version": {
        "required": true,
        "type": "integer"
      }
    },
    "loomplane_environments": {
      "id": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "project_id": {
        "required": true,
        "type": "text"
      }
    },
    "loomplane_event_cursors": {
      "floor_seq": {
        "required": true,
        "type": "bigint"
      },
      "next_seq": {
        "required": true,
        "type": "bigint"
      },
      "org_id": {
        "required": true,
        "type": "text"
      }
    },
    "loomplane_events": {
      "created_at": {
        "required": true,
        "type": "timestamp with time zone"
      },
      "data": {
        "required": true,
        "type": "jsonb"
      },
      "entity_id": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "project_id": {
        "required": true,
        "type": "text"
      },
      "sequence": {
        "required": true,
        "type": "bigint"
      },
      "type": {
        "required": true,
        "type": "text"
      }
    },
    "loomplane_fleet_authorities": {
      "cpu_millis": {
        "required": true,
        "type": "bigint"
      },
      "credential_id": {
        "required": true,
        "type": "text"
      },
      "expires_at": {
        "required": true,
        "type": "timestamp with time zone"
      },
      "external_ref": {
        "required": true,
        "type": "text"
      },
      "fleet_id": {
        "required": true,
        "type": "text"
      },
      "grant_id": {
        "required": true,
        "type": "text"
      },
      "grant_revision": {
        "required": true,
        "type": "bigint"
      },
      "hourly_cost_micros": {
        "required": true,
        "type": "bigint"
      },
      "id": {
        "required": true,
        "type": "text"
      },
      "idempotency_key": {
        "required": true,
        "type": "text"
      },
      "kind": {
        "required": true,
        "type": "text"
      },
      "memory_mib": {
        "required": true,
        "type": "bigint"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "project_id": {
        "required": true,
        "type": "text"
      },
      "released_at": {
        "required": false,
        "type": "timestamp with time zone"
      },
      "request_hash": {
        "required": true,
        "type": "text"
      },
      "snapshot": {
        "required": true,
        "type": "text"
      },
      "storage_gib": {
        "required": true,
        "type": "bigint"
      }
    },
    "loomplane_fleet_grants": {
      "created_at": {
        "required": true,
        "type": "timestamp with time zone"
      },
      "created_by": {
        "required": true,
        "type": "text"
      },
      "credential_id": {
        "required": true,
        "type": "text"
      },
      "expires_at": {
        "required": true,
        "type": "timestamp with time zone"
      },
      "fleet_id": {
        "required": true,
        "type": "text"
      },
      "id": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "project_id": {
        "required": true,
        "type": "text"
      },
      "revision": {
        "required": true,
        "type": "bigint"
      },
      "snapshot": {
        "required": true,
        "type": "text"
      },
      "stage_id": {
        "required": true,
        "type": "text"
      },
      "status": {
        "required": true,
        "type": "text"
      }
    },
    "loomplane_fleet_instances": {
      "created_at": {
        "required": true,
        "type": "timestamp with time zone"
      },
      "desired_generation": {
        "required": true,
        "type": "bigint"
      },
      "desired_release_id": {
        "required": true,
        "type": "text"
      },
      "environment_id": {
        "required": true,
        "type": "text"
      },
      "external_ref": {
        "required": true,
        "type": "text"
      },
      "fleet_id": {
        "required": true,
        "type": "text"
      },
      "id": {
        "required": true,
        "type": "text"
      },
      "observed_generation": {
        "required": true,
        "type": "bigint"
      },
      "observed_release_id": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "ownership_epoch": {
        "required": true,
        "type": "bigint"
      },
      "profile_id": {
        "required": true,
        "type": "text"
      },
      "project_id": {
        "required": true,
        "type": "text"
      },
      "revision": {
        "required": true,
        "type": "bigint"
      },
      "stage_id": {
        "required": true,
        "type": "text"
      },
      "status": {
        "required": true,
        "type": "text"
      },
      "updated_at": {
        "required": true,
        "type": "timestamp with time zone"
      }
    },
    "loomplane_fleet_namespace_attempts": {
      "authority_id": {
        "required": true,
        "type": "text"
      },
      "authorized_at": {
        "required": true,
        "type": "timestamp with time zone"
      },
      "authorized_at_ns": {
        "required": true,
        "type": "smallint"
      },
      "environment_id": {
        "required": true,
        "type": "text"
      },
      "expected_namespace_revision": {
        "required": true,
        "type": "bigint"
      },
      "expected_operation_revision": {
        "required": true,
        "type": "bigint"
      },
      "fleet_id": {
        "required": true,
        "type": "text"
      },
      "generation": {
        "required": true,
        "type": "bigint"
      },
      "id": {
        "required": true,
        "type": "text"
      },
      "input": {
        "required": true,
        "type": "jsonb"
      },
      "input_digest": {
        "required": true,
        "type": "text"
      },
      "instance_id": {
        "required": true,
        "type": "text"
      },
      "key_id": {
        "required": true,
        "type": "text"
      },
      "kind": {
        "required": true,
        "type": "text"
      },
      "operation_id": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "ownership_epoch": {
        "required": true,
        "type": "bigint"
      },
      "project_id": {
        "required": true,
        "type": "text"
      },
      "result": {
        "required": true,
        "type": "jsonb"
      },
      "result_namespace_revision": {
        "required": true,
        "type": "bigint"
      },
      "result_operation_revision": {
        "required": true,
        "type": "bigint"
      }
    },
    "loomplane_fleet_namespaces": {
      "authority": {
        "required": true,
        "type": "text"
      },
      "body": {
        "required": true,
        "type": "text"
      },
      "cloud_id": {
        "required": true,
        "type": "text"
      },
      "cluster_id": {
        "required": true,
        "type": "text"
      },
      "creation_attempt_id": {
        "required": false,
        "type": "text"
      },
      "current_attempt_id": {
        "required": false,
        "type": "text"
      },
      "environment_id": {
        "required": true,
        "type": "text"
      },
      "fleet_id": {
        "required": true,
        "type": "text"
      },
      "generation": {
        "required": true,
        "type": "bigint"
      },
      "instance_id": {
        "required": true,
        "type": "text"
      },
      "namespace": {
        "required": true,
        "type": "text"
      },
      "operation_id": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "ownership_epoch": {
        "required": true,
        "type": "bigint"
      },
      "project_id": {
        "required": true,
        "type": "text"
      },
      "revision": {
        "required": true,
        "type": "bigint"
      },
      "snapshot_digest": {
        "required": true,
        "type": "text"
      }
    },
    "loomplane_fleet_releases": {
      "created_at": {
        "required": true,
        "type": "timestamp with time zone"
      },
      "digest": {
        "required": true,
        "type": "text"
      },
      "fleet_id": {
        "required": true,
        "type": "text"
      },
      "id": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "project_id": {
        "required": true,
        "type": "text"
      },
      "snapshot": {
        "required": true,
        "type": "text"
      }
    },
    "loomplane_fleet_stage_clouds": {
      "cloud_id": {
        "required": true,
        "type": "text"
      },
      "fleet_id": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "stage_id": {
        "required": true,
        "type": "text"
      }
    },
    "loomplane_fleet_stages": {
      "created_at": {
        "required": true,
        "type": "timestamp with time zone"
      },
      "environment_kind": {
        "required": true,
        "type": "text"
      },
      "fleet_id": {
        "required": true,
        "type": "text"
      },
      "id": {
        "required": true,
        "type": "text"
      },
      "max_instances": {
        "required": true,
        "type": "integer"
      },
      "name": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "policy_revision": {
        "required": true,
        "type": "bigint"
      },
      "project_id": {
        "required": true,
        "type": "text"
      }
    },
    "loomplane_fleets": {
      "control_project_id": {
        "required": false,
        "type": "text"
      },
      "created_at": {
        "required": true,
        "type": "timestamp with time zone"
      },
      "id": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "project_id": {
        "required": true,
        "type": "text"
      },
      "revision": {
        "required": true,
        "type": "bigint"
      },
      "status": {
        "required": true,
        "type": "text"
      },
      "updated_at": {
        "required": true,
        "type": "timestamp with time zone"
      }
    },
    "loomplane_managed_resources": {
      "account_id": {
        "required": true,
        "type": "text"
      },
      "body": {
        "required": true,
        "type": "jsonb"
      },
      "cloud_id": {
        "required": false,
        "type": "text"
      },
      "connection_id": {
        "required": false,
        "type": "text"
      },
      "deleted_at": {
        "required": false,
        "type": "timestamp with time zone"
      },
      "external_id": {
        "required": true,
        "type": "text"
      },
      "id": {
        "required": true,
        "type": "text"
      },
      "incarnation": {
        "required": true,
        "type": "text"
      },
      "kind": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "ownership": {
        "required": true,
        "type": "text"
      },
      "provider": {
        "required": true,
        "type": "text"
      },
      "revision": {
        "required": true,
        "type": "bigint"
      },
      "status": {
        "required": true,
        "type": "text"
      }
    },
    "loomplane_runtime_attachments": {
      "body": {
        "required": true,
        "type": "jsonb"
      },
      "cloud_id": {
        "required": true,
        "type": "text"
      },
      "cluster_id": {
        "required": true,
        "type": "text"
      },
      "deleted_at": {
        "required": false,
        "type": "timestamp with time zone"
      },
      "endpoint_fingerprint": {
        "required": true,
        "type": "text"
      },
      "id": {
        "required": true,
        "type": "text"
      },
      "namespace": {
        "required": true,
        "type": "text"
      },
      "namespace_uid": {
        "required": true,
        "type": "text"
      },
      "org_id": {
        "required": true,
        "type": "text"
      },
      "project_id": {
        "required": true,
        "type": "text"
      },
      "revision": {
        "required": true,
        "type": "bigint"
      },
      "runtime_connection_id": {
        "required": true,
        "type": "text"
      },
      "service_account_uid": {
        "required": true,
        "type": "text"
      },
      "trust_fingerprint": {
        "required": true,
        "type": "text"
      }
    }
  },
  "foreign_keys": [
    {
      "columns": [
        "org_id",
        "connection_id"
      ],
      "table": "loomplane_cloud_targets",
      "target": "loomplane_cloud_connections",
      "target_columns": [
        "org_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "cloud_id"
      ],
      "table": "loomplane_cloud_project_grants",
      "target": "loomplane_cloud_profiles",
      "target_columns": [
        "org_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "project_id"
      ],
      "table": "loomplane_cloud_project_grants",
      "target": "loomplane_projects",
      "target_columns": [
        "org_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "cloud_id"
      ],
      "table": "loomplane_managed_resources",
      "target": "loomplane_cloud_profiles",
      "target_columns": [
        "org_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "connection_id"
      ],
      "table": "loomplane_managed_resources",
      "target": "loomplane_cloud_connections",
      "target_columns": [
        "org_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "cloud_id",
        "cluster_id"
      ],
      "table": "loomplane_runtime_attachments",
      "target": "loomplane_managed_resources",
      "target_columns": [
        "org_id",
        "cloud_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "project_id"
      ],
      "table": "loomplane_runtime_attachments",
      "target": "loomplane_projects",
      "target_columns": [
        "org_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "runtime_connection_id"
      ],
      "table": "loomplane_runtime_attachments",
      "target": "loomplane_cloud_connections",
      "target_columns": [
        "org_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "cloud_id"
      ],
      "table": "loomplane_cluster_operations",
      "target": "loomplane_cloud_profiles",
      "target_columns": [
        "org_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "cloud_id",
        "resource_id"
      ],
      "table": "loomplane_cluster_operations",
      "target": "loomplane_managed_resources",
      "target_columns": [
        "org_id",
        "cloud_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "operation_id"
      ],
      "table": "loomplane_cluster_operation_steps",
      "target": "loomplane_cluster_operations",
      "target_columns": [
        "org_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "project_id"
      ],
      "table": "loomplane_fleets",
      "target": "loomplane_projects",
      "target_columns": [
        "org_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "control_project_id"
      ],
      "table": "loomplane_fleets",
      "target": "loomplane_projects",
      "target_columns": [
        "org_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "project_id",
        "fleet_id"
      ],
      "table": "loomplane_fleet_stages",
      "target": "loomplane_fleets",
      "target_columns": [
        "org_id",
        "project_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "fleet_id",
        "stage_id"
      ],
      "table": "loomplane_fleet_stage_clouds",
      "target": "loomplane_fleet_stages",
      "target_columns": [
        "org_id",
        "fleet_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "cloud_id"
      ],
      "table": "loomplane_fleet_stage_clouds",
      "target": "loomplane_cloud_profiles",
      "target_columns": [
        "org_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "project_id",
        "fleet_id"
      ],
      "table": "loomplane_fleet_instances",
      "target": "loomplane_fleets",
      "target_columns": [
        "org_id",
        "project_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "fleet_id",
        "stage_id"
      ],
      "table": "loomplane_fleet_instances",
      "target": "loomplane_fleet_stages",
      "target_columns": [
        "org_id",
        "fleet_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "project_id",
        "environment_id"
      ],
      "table": "loomplane_fleet_instances",
      "target": "loomplane_environments",
      "target_columns": [
        "org_id",
        "project_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "project_id",
        "fleet_id"
      ],
      "table": "loomplane_fleet_releases",
      "target": "loomplane_fleets",
      "target_columns": [
        "org_id",
        "project_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "project_id",
        "fleet_id"
      ],
      "table": "loomplane_fleet_grants",
      "target": "loomplane_fleets",
      "target_columns": [
        "org_id",
        "project_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "fleet_id",
        "stage_id"
      ],
      "table": "loomplane_fleet_grants",
      "target": "loomplane_fleet_stages",
      "target_columns": [
        "org_id",
        "fleet_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "project_id",
        "fleet_id"
      ],
      "table": "loomplane_fleet_authorities",
      "target": "loomplane_fleets",
      "target_columns": [
        "org_id",
        "project_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "fleet_id",
        "grant_id"
      ],
      "table": "loomplane_fleet_authorities",
      "target": "loomplane_fleet_grants",
      "target_columns": [
        "org_id",
        "fleet_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "operation_id"
      ],
      "table": "loomplane_fleet_namespaces",
      "target": "loomplane_cluster_operations",
      "target_columns": [
        "org_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "project_id",
        "fleet_id"
      ],
      "table": "loomplane_fleet_namespaces",
      "target": "loomplane_fleets",
      "target_columns": [
        "org_id",
        "project_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "fleet_id",
        "instance_id"
      ],
      "table": "loomplane_fleet_namespaces",
      "target": "loomplane_fleet_instances",
      "target_columns": [
        "org_id",
        "fleet_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "project_id",
        "environment_id"
      ],
      "table": "loomplane_fleet_namespaces",
      "target": "loomplane_environments",
      "target_columns": [
        "org_id",
        "project_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "operation_id"
      ],
      "table": "loomplane_fleet_namespace_attempts",
      "target": "loomplane_cluster_operations",
      "target_columns": [
        "org_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "project_id",
        "fleet_id"
      ],
      "table": "loomplane_fleet_namespace_attempts",
      "target": "loomplane_fleets",
      "target_columns": [
        "org_id",
        "project_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "fleet_id",
        "instance_id"
      ],
      "table": "loomplane_fleet_namespace_attempts",
      "target": "loomplane_fleet_instances",
      "target_columns": [
        "org_id",
        "fleet_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "project_id",
        "environment_id"
      ],
      "table": "loomplane_fleet_namespace_attempts",
      "target": "loomplane_environments",
      "target_columns": [
        "org_id",
        "project_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "cloud_id"
      ],
      "table": "loomplane_cloud_targets",
      "target": "loomplane_cloud_profiles",
      "target_columns": [
        "org_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "cloud_id",
        "managed_resource_id"
      ],
      "table": "loomplane_cloud_targets",
      "target": "loomplane_managed_resources",
      "target_columns": [
        "org_id",
        "cloud_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "cloud_id",
        "location_id"
      ],
      "table": "loomplane_cloud_targets",
      "target": "loomplane_managed_resources",
      "target_columns": [
        "org_id",
        "cloud_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "operation_id",
        "creation_attempt_id"
      ],
      "table": "loomplane_fleet_namespaces",
      "target": "loomplane_fleet_namespace_attempts",
      "target_columns": [
        "org_id",
        "operation_id",
        "id"
      ]
    },
    {
      "columns": [
        "org_id",
        "operation_id",
        "current_attempt_id"
      ],
      "table": "loomplane_fleet_namespaces",
      "target": "loomplane_fleet_namespace_attempts",
      "target_columns": [
        "org_id",
        "operation_id",
        "id"
      ]
    }
  ],
  "functions": {
    "loomplane_append_event": {
      "arguments": [
        "text",
        "text",
        "text",
        "text",
        "jsonb"
      ],
      "body": "a2806614d7cb7c179957f7c263a68c1fe08bbf7b3ccbc573128b574148f2d534",
      "language": "plpgsql",
      "names": [
        "org",
        "project",
        "event_type",
        "entity",
        "payload"
      ],
      "return": "bigint",
      "source": "20261016000003_persist_realtime_events.go",
      "volatility": "v"
    },
    "loomplane_cloud_location_target_guard": {
      "arguments": [],
      "body": "147e19939c32ca337f785a4561f6eae0f01bff4fae24c9324a63c7fee000175c",
      "language": "plpgsql",
      "names": [],
      "return": "trigger",
      "source": "20261207001600_cloud_locations.go",
      "volatility": "v"
    },
    "loomplane_cloud_tags_valid": {
      "arguments": [
        "text[]"
      ],
      "body": "806f7a08988032c77297c863b6b1979d2b94c27b587ee24fee50e88e5eeff949",
      "language": "sql",
      "names": [
        "tags"
      ],
      "return": "boolean",
      "source": "20261029000400_cloud_tags.go",
      "volatility": "i"
    },
    "loomplane_cluster_controls_current": {
      "arguments": [
        "text",
        "text"
      ],
      "body": "5660c5034c3a36211914a9bb1728ec8e76024a6c204106bc102f8cc5c050faa4",
      "language": "plpgsql",
      "names": [
        "tenant",
        "resource"
      ],
      "return": "boolean",
      "source": "20261207001500_observed_accounting_metadata.go",
      "volatility": "s"
    },
    "loomplane_cluster_retiring_attachment": {
      "arguments": [],
      "body": "4550924b3a7a684943389a5722a2c762e3c5fa0e2b7f299e7627322bb079c073",
      "language": "plpgsql",
      "names": [],
      "return": "trigger",
      "source": "20261126000900_cluster_retirement_receipts.go",
      "volatility": "v"
    },
    "loomplane_fleet_namespace_attempt_guard": {
      "arguments": [],
      "body": "fe0b1e233a21fb46905b960c44e4151143910d46a62d45b4eb854839d200a8b0",
      "language": "plpgsql",
      "names": [],
      "return": "trigger",
      "source": "20261223001000_fleet_namespace_attempts.go",
      "volatility": "v"
    },
    "loomplane_fleet_namespace_identity": {
      "arguments": [],
      "body": "96ea42eff7db05dc798943ba430f5407d017e106c8a13a57052a3a486b7ae343",
      "language": "plpgsql",
      "names": [],
      "return": "trigger",
      "source": "20261223001000_fleet_namespace_attempts.go",
      "volatility": "v"
    },
    "loomplane_managed_target_guard": {
      "arguments": [],
      "body": "a8218398987c122cf1c0b8345170f82a0952c8e7cfe6d3fc3fc0c37857c4476d",
      "language": "plpgsql",
      "names": [],
      "return": "trigger",
      "source": "20261126000400_managed_clusters.go",
      "volatility": "v"
    },
    "loomplane_platform_projection_guard": {
      "arguments": [],
      "body": "db3ee1addb449d44d0ef9f9b16dbbe06835ad3dfbb2debfbcde0593a68874f18",
      "language": "plpgsql",
      "names": [],
      "return": "trigger",
      "source": "20261223000100_platform_cloud.go",
      "volatility": "v"
    },
    "loomplane_runtime_access_operation_guard": {
      "arguments": [],
      "body": "bdd9146cf0261bc1a86b8d012275ed8080feb00bbafce858100109aa09f78f1c",
      "language": "plpgsql",
      "names": [],
      "return": "trigger",
      "source": "20261206000300_runtime_access.go",
      "volatility": "v"
    },
    "loomplane_runtime_attachment_guard": {
      "arguments": [],
      "body": "785f4db72e1b377612488a4dc5cc6604a13d83e31a790dce752a67eba52e6fd6",
      "language": "plpgsql",
      "names": [],
      "return": "trigger",
      "source": "20261126000400_managed_clusters.go",
      "volatility": "v"
    },
    "loomplane_state_event_trigger": {
      "arguments": [],
      "body": "8ee0b0fd5457e1e7168cd12b4f9d7a17154a6b5274ce640bdb633e95952573fb",
      "language": "plpgsql",
      "names": [],
      "return": "trigger",
      "source": "20261016000003_persist_realtime_events.go",
      "volatility": "v"
    },
    "loomplane_variable_source_gate": {
      "arguments": [],
      "body": "c38efb5f6b507c818404cc09058f9c9f0d7fb5e083601b78674df619a67ca0e5",
      "language": "plpgsql",
      "names": [],
      "return": "trigger",
      "source": "20261208000600_variable_versions.go",
      "volatility": "v"
    }
  },
  "indexes": [
    {
      "columns": [
        "id"
      ],
      "primary": true,
      "table": "loomplane_environments"
    },
    {
      "columns": [
        "project_id",
        "name"
      ],
      "primary": false,
      "table": "loomplane_environments"
    },
    {
      "columns": [
        "id"
      ],
      "primary": true,
      "table": "loomplane_cloud_connections"
    },
    {
      "columns": [
        "org_id",
        "name"
      ],
      "primary": false,
      "table": "loomplane_cloud_connections"
    },
    {
      "columns": [
        "org_id"
      ],
      "primary": true,
      "table": "loomplane_event_cursors"
    },
    {
      "columns": [
        "org_id",
        "sequence"
      ],
      "primary": true,
      "table": "loomplane_events"
    },
    {
      "columns": [
        "id"
      ],
      "primary": true,
      "table": "loomplane_cloud_profiles"
    },
    {
      "columns": [
        "org_id",
        "project_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_cloud_profiles"
    },
    {
      "columns": [
        "org_id",
        "project_id",
        "name"
      ],
      "primary": false,
      "table": "loomplane_cloud_profiles"
    },
    {
      "columns": [
        "id"
      ],
      "primary": true,
      "table": "loomplane_cloud_targets"
    },
    {
      "columns": [
        "org_id",
        "project_id",
        "cloud_id",
        "name"
      ],
      "primary": false,
      "table": "loomplane_cloud_targets"
    },
    {
      "columns": [
        "org_id",
        "project_id",
        "cloud_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_cloud_targets"
    },
    {
      "columns": [
        "org_id",
        "cloud_id",
        "project_id"
      ],
      "primary": true,
      "table": "loomplane_cloud_project_grants"
    },
    {
      "columns": [
        "id"
      ],
      "primary": true,
      "table": "loomplane_managed_resources"
    },
    {
      "columns": [
        "org_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_managed_resources"
    },
    {
      "columns": [
        "org_id",
        "cloud_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_managed_resources"
    },
    {
      "columns": [
        "org_id",
        "provider",
        "account_id",
        "kind",
        "external_id"
      ],
      "primary": false,
      "table": "loomplane_managed_resources"
    },
    {
      "columns": [
        "id"
      ],
      "primary": true,
      "table": "loomplane_runtime_attachments"
    },
    {
      "columns": [
        "org_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_runtime_attachments"
    },
    {
      "columns": [
        "org_id",
        "cluster_id",
        "namespace_uid"
      ],
      "primary": false,
      "table": "loomplane_runtime_attachments"
    },
    {
      "columns": [
        "id"
      ],
      "primary": true,
      "table": "loomplane_cluster_operations"
    },
    {
      "columns": [
        "org_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_cluster_operations"
    },
    {
      "columns": [
        "org_id",
        "operation_id",
        "name"
      ],
      "primary": true,
      "table": "loomplane_cluster_operation_steps"
    },
    {
      "columns": [
        "id"
      ],
      "primary": true,
      "table": "loomplane_fleets"
    },
    {
      "columns": [
        "org_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_fleets"
    },
    {
      "columns": [
        "org_id",
        "project_id"
      ],
      "primary": false,
      "table": "loomplane_fleets"
    },
    {
      "columns": [
        "org_id",
        "project_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_fleets"
    },
    {
      "columns": [
        "id"
      ],
      "primary": true,
      "table": "loomplane_fleet_stages"
    },
    {
      "columns": [
        "org_id",
        "fleet_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_fleet_stages"
    },
    {
      "columns": [
        "org_id",
        "fleet_id",
        "name"
      ],
      "primary": false,
      "table": "loomplane_fleet_stages"
    },
    {
      "columns": [
        "org_id",
        "stage_id",
        "cloud_id"
      ],
      "primary": true,
      "table": "loomplane_fleet_stage_clouds"
    },
    {
      "columns": [
        "id"
      ],
      "primary": true,
      "table": "loomplane_fleet_instances"
    },
    {
      "columns": [
        "org_id",
        "fleet_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_fleet_instances"
    },
    {
      "columns": [
        "org_id",
        "fleet_id",
        "external_ref"
      ],
      "primary": false,
      "table": "loomplane_fleet_instances"
    },
    {
      "columns": [
        "environment_id"
      ],
      "primary": false,
      "table": "loomplane_fleet_instances"
    },
    {
      "columns": [
        "id"
      ],
      "primary": true,
      "table": "loomplane_fleet_releases"
    },
    {
      "columns": [
        "org_id",
        "fleet_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_fleet_releases"
    },
    {
      "columns": [
        "org_id",
        "fleet_id",
        "digest"
      ],
      "primary": false,
      "table": "loomplane_fleet_releases"
    },
    {
      "columns": [
        "id"
      ],
      "primary": true,
      "table": "loomplane_fleet_grants"
    },
    {
      "columns": [
        "org_id",
        "fleet_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_fleet_grants"
    },
    {
      "columns": [
        "id"
      ],
      "primary": true,
      "table": "loomplane_fleet_authorities"
    },
    {
      "columns": [
        "org_id",
        "fleet_id",
        "credential_id",
        "kind",
        "idempotency_key"
      ],
      "primary": false,
      "table": "loomplane_fleet_authorities"
    },
    {
      "columns": [
        "org_id",
        "fleet_id",
        "grant_id",
        "external_ref"
      ],
      "primary": false,
      "table": "loomplane_fleet_authorities"
    },
    {
      "columns": [
        "operation_id"
      ],
      "primary": true,
      "table": "loomplane_fleet_namespaces"
    },
    {
      "columns": [
        "org_id",
        "instance_id",
        "ownership_epoch"
      ],
      "primary": false,
      "table": "loomplane_fleet_namespaces"
    },
    {
      "columns": [
        "org_id",
        "cluster_id",
        "namespace"
      ],
      "primary": false,
      "table": "loomplane_fleet_namespaces"
    },
    {
      "columns": [
        "id"
      ],
      "primary": true,
      "table": "loomplane_fleet_namespace_attempts"
    },
    {
      "columns": [
        "org_id",
        "operation_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_fleet_namespace_attempts"
    },
    {
      "columns": [
        "org_id",
        "operation_id",
        "result_namespace_revision"
      ],
      "primary": false,
      "table": "loomplane_fleet_namespace_attempts"
    },
    {
      "columns": [
        "org_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_cloud_profiles"
    },
    {
      "columns": [
        "org_id",
        "cloud_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_cloud_targets"
    },
    {
      "columns": [
        "org_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_cloud_connections"
    },
    {
      "columns": [
        "org_id",
        "project_id",
        "id"
      ],
      "primary": false,
      "table": "loomplane_environments"
    }
  ],
  "namespace_evidence_version": 1,
  "triggers": [
    {
      "arguments": "",
      "function": "loomplane_runtime_attachment_guard",
      "name": "loomplane_runtime_attachment_guard",
      "table": "loomplane_runtime_attachments",
      "type": 23
    },
    {
      "arguments": "",
      "function": "loomplane_managed_target_guard",
      "name": "loomplane_managed_target_guard",
      "table": "loomplane_cloud_targets",
      "type": 23
    },
    {
      "arguments": "",
      "function": "loomplane_cluster_retiring_attachment",
      "name": "loomplane_cluster_retiring_attachment",
      "table": "loomplane_runtime_attachments",
      "type": 23
    },
    {
      "arguments": "",
      "function": "loomplane_runtime_access_operation_guard",
      "name": "loomplane_runtime_access_operation_guard",
      "table": "loomplane_cluster_operations",
      "type": 23
    },
    {
      "arguments": "",
      "function": "loomplane_cloud_location_target_guard",
      "name": "loomplane_cloud_location_target_guard",
      "table": "loomplane_cloud_targets",
      "type": 23
    },
    {
      "arguments": "",
      "function": "loomplane_fleet_namespace_identity",
      "name": "loomplane_fleet_namespace_identity",
      "table": "loomplane_fleet_namespaces",
      "type": 31
    },
    {
      "arguments": "",
      "function": "loomplane_platform_projection_guard",
      "name": "loomplane_cloud_a_target_platform_guard",
      "table": "loomplane_cloud_targets",
      "type": 31
    },
    {
      "arguments": "",
      "function": "loomplane_fleet_namespace_attempt_guard",
      "name": "loomplane_fleet_namespace_attempt_guard",
      "table": "loomplane_fleet_namespace_attempts",
      "type": 31
    },
    {
      "arguments": "636c6f75645f74617267657400",
      "function": "loomplane_state_event_trigger",
      "name": "loomplane_state_event",
      "table": "loomplane_cloud_targets",
      "type": 29
    },
    {
      "arguments": "636f6e6e656374696f6e00",
      "function": "loomplane_state_event_trigger",
      "name": "loomplane_state_event",
      "table": "loomplane_cloud_connections",
      "type": 29
    }
  ],
  "write_events": {
    "loomplane_cloud_connections": 4,
    "loomplane_cloud_targets": 4,
    "loomplane_cluster_operation_steps": 20,
    "loomplane_cluster_operations": 20,
    "loomplane_event_cursors": 20,
    "loomplane_events": 4,
    "loomplane_fleet_namespace_attempts": 4,
    "loomplane_fleet_namespaces": 20,
    "loomplane_runtime_attachments": 4
  }
}`
