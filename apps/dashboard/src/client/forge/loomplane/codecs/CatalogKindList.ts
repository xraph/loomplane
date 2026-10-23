import type { Codec } from '../codec-runtime';
import { codec_CatalogKindList_kinds } from './CatalogKindList.kinds';

export const codec_CatalogKindList: Codec = {"kind": "object", "fields": {"kinds": {"ts": "kinds", "codec": () => codec_CatalogKindList_kinds}}, "required": ["kinds"]};
