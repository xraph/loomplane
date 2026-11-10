import type { Codec } from '../codec-runtime';
import { codec_DeletionPreview_removes } from './DeletionPreview.removes';

export const codec_DeletionPreview: Codec = {"kind": "object", "fields": {"blocked": {"ts": "blocked"}, "removes": {"ts": "removes", "codec": () => codec_DeletionPreview_removes}}, "required": ["removes"]};
