import type { Codec } from '../codec-runtime';
import { codec_CatalogconnectUse } from './CatalogconnectUse';

export const codec_ServiceUseList_uses: Codec = {"kind": "array", "items": () => codec_CatalogconnectUse};
