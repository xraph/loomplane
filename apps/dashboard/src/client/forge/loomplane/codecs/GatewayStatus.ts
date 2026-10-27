import type { Codec } from '../codec-runtime';
import { codec_GatewayStatus_instances } from './GatewayStatus.instances';
import { codec_GatewayStatus_routes } from './GatewayStatus.routes';

export const codec_GatewayStatus: Codec = {"kind": "object", "fields": {"environment_id": {"ts": "environmentId"}, "id": {"ts": "id"}, "instances": {"ts": "instances", "codec": () => codec_GatewayStatus_instances}, "name": {"ts": "name"}, "provider": {"ts": "provider"}, "region": {"ts": "region"}, "routes": {"ts": "routes", "codec": () => codec_GatewayStatus_routes}, "snapshot_version": {"ts": "snapshotVersion"}}, "required": ["environment_id", "id", "instances", "name", "provider", "region", "routes", "snapshot_version"]};
