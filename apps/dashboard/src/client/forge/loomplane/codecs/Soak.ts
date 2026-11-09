import type { Codec } from '../codec-runtime';
import { codec_Soak_reasons } from './Soak.reasons';
import { codec_Soak_services } from './Soak.services';

export const codec_Soak: Codec = {"kind": "object", "fields": {"duration": {"ts": "duration"}, "environment": {"ts": "environment"}, "healthy": {"ts": "healthy"}, "reasons": {"ts": "reasons", "codec": () => codec_Soak_reasons}, "running": {"ts": "running"}, "services": {"ts": "services", "codec": () => codec_Soak_services}, "since": {"ts": "since"}, "unverified": {"ts": "unverified"}}, "required": ["duration", "environment", "healthy", "reasons", "running", "services", "since", "unverified"]};
