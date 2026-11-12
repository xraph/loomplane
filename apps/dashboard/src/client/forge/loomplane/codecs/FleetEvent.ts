import type { Codec } from '../codec-runtime';
import { codec_LifecycleEvent } from './LifecycleEvent';
import { codec_ReleaseEvent } from './ReleaseEvent';
import { codec_ScalingEvent } from './ScalingEvent';

export const codec_FleetEvent: Codec = {"kind": "object", "fields": {"component": {"ts": "component"}, "environment_id": {"ts": "environmentId"}, "fleet_id": {"ts": "fleetId"}, "generation": {"ts": "generation"}, "id": {"ts": "id"}, "instance_id": {"ts": "instanceId"}, "kind": {"ts": "kind"}, "lifecycle": {"ts": "lifecycle", "codec": () => codec_LifecycleEvent}, "occurred_at": {"ts": "occurredAt"}, "operation_id": {"ts": "operationId"}, "org_id": {"ts": "orgId"}, "release": {"ts": "release", "codec": () => codec_ReleaseEvent}, "release_id": {"ts": "releaseId"}, "scaling": {"ts": "scaling", "codec": () => codec_ScalingEvent}, "sequence": {"ts": "sequence"}, "version": {"ts": "version"}}, "required": ["environment_id", "fleet_id", "generation", "id", "instance_id", "kind", "occurred_at", "org_id", "sequence", "version"]};
