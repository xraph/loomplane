import type { Codec } from '../codec-runtime';
import { codec_Operation } from './Operation';

export const codec_OperationList_operations: Codec = {"kind": "array", "items": () => codec_Operation};
