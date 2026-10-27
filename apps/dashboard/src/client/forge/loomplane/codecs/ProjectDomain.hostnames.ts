import type { Codec } from '../codec-runtime';
import { codec_HostnameUse } from './HostnameUse';

export const codec_ProjectDomain_hostnames: Codec = {"kind": "array", "items": () => codec_HostnameUse};
