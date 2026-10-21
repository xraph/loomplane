import type { Codec } from '../codec-runtime';
import { codec_RouteOperation } from './RouteOperation';

export const codec_DomainOperationList_operations: Codec = {"kind": "array", "items": () => codec_RouteOperation};
