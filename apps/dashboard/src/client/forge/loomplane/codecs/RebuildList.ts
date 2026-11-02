import type { Codec } from '../codec-runtime';
import { codec_RebuildList_rebuilds } from './RebuildList.rebuilds';

export const codec_RebuildList: Codec = {"kind": "object", "fields": {"rebuilds": {"ts": "rebuilds", "codec": () => codec_RebuildList_rebuilds}}, "required": ["rebuilds"]};
