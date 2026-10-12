import type { Codec } from '../codec-runtime';
import { codec_DeployedList_sources } from './DeployedList.sources';

export const codec_DeployedList: Codec = {"kind": "object", "fields": {"sources": {"ts": "sources", "codec": () => codec_DeployedList_sources}}, "required": ["sources"]};
