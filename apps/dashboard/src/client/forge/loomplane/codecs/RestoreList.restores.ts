import type { Codec } from '../codec-runtime';
import { codec_Restore } from './Restore';

export const codec_RestoreList_restores: Codec = {"kind": "array", "items": () => codec_Restore};
