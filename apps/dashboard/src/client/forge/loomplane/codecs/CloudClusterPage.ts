import type { Codec } from '../codec-runtime';
import { codec_CloudClusterPage_items } from './CloudClusterPage.items';

export const codec_CloudClusterPage: Codec = {"kind": "object", "fields": {"items": {"ts": "items", "codec": () => codec_CloudClusterPage_items}, "next_cursor": {"ts": "nextCursor"}}, "required": ["items"]};
