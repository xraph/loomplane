import type { Codec } from '../codec-runtime';
import { codec_ExpansionList_expansions } from './ExpansionList.expansions';

export const codec_ExpansionList: Codec = {"kind": "object", "fields": {"expansions": {"ts": "expansions", "codec": () => codec_ExpansionList_expansions}}, "required": ["expansions"]};
