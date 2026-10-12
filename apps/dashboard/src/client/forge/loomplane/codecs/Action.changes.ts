import type { Codec } from '../codec-runtime';
import { codec_Change } from './Change';

export const codec_Action_changes: Codec = {"kind": "array", "items": () => codec_Change};
