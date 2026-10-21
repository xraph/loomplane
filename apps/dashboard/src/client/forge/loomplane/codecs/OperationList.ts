import type { Codec } from '../codec-runtime';
import { codec_OperationList_operations } from './OperationList.operations';

export const codec_OperationList: Codec = {"kind": "object", "fields": {"operations": {"ts": "operations", "codec": () => codec_OperationList_operations}}, "required": ["operations"]};
