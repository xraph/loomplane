import type { Codec } from '../codec-runtime';
import { codec_Revision } from './Revision';

export const codec_RevisionList_revisions: Codec = {"kind": "array", "items": () => codec_Revision};
