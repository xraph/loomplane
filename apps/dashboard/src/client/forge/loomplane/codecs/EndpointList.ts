import type { Codec } from '../codec-runtime';
import { codec_EndpointList_endpoints } from './EndpointList.endpoints';

export const codec_EndpointList: Codec = {"kind": "object", "fields": {"endpoints": {"ts": "endpoints", "codec": () => codec_EndpointList_endpoints}}, "required": ["endpoints"]};
