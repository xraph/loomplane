import type { Codec } from '../codec-runtime';
import { codec_UsageSource_services } from './UsageSource.services';

export const codec_UsageSource: Codec = {"kind": "object", "fields": {"cloud": {"ts": "cloud"}, "connection_id": {"ts": "connectionId"}, "name": {"ts": "name"}, "reason": {"ts": "reason"}, "services": {"ts": "services", "codec": () => codec_UsageSource_services}, "status": {"ts": "status"}}, "required": ["cloud", "connection_id", "name", "status"]};
