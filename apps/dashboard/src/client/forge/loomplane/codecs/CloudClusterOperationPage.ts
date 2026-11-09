import type { Codec } from '../codec-runtime';
import { codec_CloudClusterOperationPage_items } from './CloudClusterOperationPage.items';

export const codec_CloudClusterOperationPage: Codec = {"kind": "object", "fields": {"items": {"ts": "items", "codec": () => codec_CloudClusterOperationPage_items}, "next_cursor": {"ts": "nextCursor"}}, "required": ["items"]};
