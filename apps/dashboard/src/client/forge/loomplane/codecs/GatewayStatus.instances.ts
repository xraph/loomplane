import type { Codec } from '../codec-runtime';
import { codec_InstanceStatus } from './InstanceStatus';

export const codec_GatewayStatus_instances: Codec = {"kind": "array", "items": () => codec_InstanceStatus};
