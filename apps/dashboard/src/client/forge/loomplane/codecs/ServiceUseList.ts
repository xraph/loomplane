import type { Codec } from '../codec-runtime';
import { codec_ServiceUseList_uses } from './ServiceUseList.uses';

export const codec_ServiceUseList: Codec = {"kind": "object", "fields": {"uses": {"ts": "uses", "codec": () => codec_ServiceUseList_uses}}, "required": ["uses"]};
