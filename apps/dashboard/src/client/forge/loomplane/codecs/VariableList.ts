import type { Codec } from '../codec-runtime';
import { codec_VariableList_variables } from './VariableList.variables';

export const codec_VariableList: Codec = {"kind": "object", "fields": {"variables": {"ts": "variables", "codec": () => codec_VariableList_variables}}, "required": ["variables"]};
