import type { Codec } from '../codec-runtime';
import { codec_Variable } from './Variable';

export const codec_VariableList_variables: Codec = {"kind": "array", "items": () => codec_Variable};
