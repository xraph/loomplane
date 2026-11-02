import type { Codec } from '../codec-runtime';
import { codec_HostnameUse } from './HostnameUse';

export const codec_ZoneOverview_hostnames: Codec = {"kind": "array", "items": () => codec_HostnameUse};
