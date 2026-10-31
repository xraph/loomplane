import type { Codec } from '../codec-runtime';
import { codec_CloudPlacement } from './CloudPlacement';
import { codec_ClusterSchedulingProfile } from './ClusterSchedulingProfile';
import { codec_ClusterWorkloadInput } from './ClusterWorkloadInput';

export const codec_ClusterWorkloadDemand: Codec = {"kind": "object", "fields": {"connection_generation": {"ts": "connectionGeneration"}, "input": {"ts": "input", "codec": () => codec_ClusterWorkloadInput}, "mode": {"ts": "mode"}, "placement": {"ts": "placement", "codec": () => codec_CloudPlacement}, "profile": {"ts": "profile", "codec": () => codec_ClusterSchedulingProfile}, "schema_version": {"ts": "schemaVersion"}}, "required": ["connection_generation", "input", "mode", "placement", "schema_version"]};
