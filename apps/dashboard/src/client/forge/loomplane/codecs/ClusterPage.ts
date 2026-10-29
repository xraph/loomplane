import type { Codec } from '../codec-runtime';
import { codec_ClusterPage_items } from './ClusterPage.items';

export const codec_ClusterPage: Codec = {"kind": "object", "fields": {"items": {"ts": "items", "codec": () => codec_ClusterPage_items}, "next_cursor": {"ts": "nextCursor"}}, "required": ["items"]};
