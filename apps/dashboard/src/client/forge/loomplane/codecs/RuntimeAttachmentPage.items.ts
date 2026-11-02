import type { Codec } from '../codec-runtime';
import { codec_RuntimeAttachmentSummary } from './RuntimeAttachmentSummary';

export const codec_RuntimeAttachmentPage_items: Codec = {"kind": "array", "items": () => codec_RuntimeAttachmentSummary};
