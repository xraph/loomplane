import type { Codec } from '../codec-runtime';
import { codec_InstancePage_items } from './InstancePage.items';

export const codec_InstancePage: Codec = {"kind": "object", "fields": {"items": {"ts": "items", "codec": () => codec_InstancePage_items}, "next_cursor": {"ts": "nextCursor"}}, "required": ["items"]};
