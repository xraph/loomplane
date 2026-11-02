import type { Codec } from '../codec-runtime';
import { codec_RuntimeAttachmentPage_items } from './RuntimeAttachmentPage.items';

export const codec_RuntimeAttachmentPage: Codec = {"kind": "object", "fields": {"cluster_id": {"ts": "clusterId"}, "items": {"ts": "items", "codec": () => codec_RuntimeAttachmentPage_items}}, "required": ["cluster_id", "items"]};
