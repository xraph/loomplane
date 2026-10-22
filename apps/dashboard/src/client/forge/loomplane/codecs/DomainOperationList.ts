import type { Codec } from '../codec-runtime';
import { codec_DomainOperationList_operations } from './DomainOperationList.operations';

export const codec_DomainOperationList: Codec = {"kind": "object", "fields": {"operations": {"ts": "operations", "codec": () => codec_DomainOperationList_operations}}, "required": ["operations"]};
