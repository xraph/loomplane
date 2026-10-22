import type { Codec } from '../codec-runtime';
import { codec_KindInfo } from './KindInfo';

export const codec_CatalogKindList_kinds: Codec = {"kind": "array", "items": () => codec_KindInfo};
