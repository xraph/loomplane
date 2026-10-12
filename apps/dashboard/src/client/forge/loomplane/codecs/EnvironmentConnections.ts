import type { Codec } from '../codec-runtime';
import { codec_EnvironmentConnections_connections } from './EnvironmentConnections.connections';

export const codec_EnvironmentConnections: Codec = {"kind": "object", "fields": {"connections": {"ts": "connections", "codec": () => codec_EnvironmentConnections_connections}, "environment": {"ts": "environment"}}, "required": ["connections", "environment"]};
