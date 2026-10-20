import type { Codec } from '../codec-runtime';
import { codec_RetentionPoint } from './RetentionPoint';

export const codec_RetentionPreview_points: Codec = {"kind": "array", "items": () => codec_RetentionPoint};
