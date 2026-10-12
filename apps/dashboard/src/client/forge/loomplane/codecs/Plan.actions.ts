import type { Codec } from '../codec-runtime';
import { codec_Action } from './Action';

export const codec_Plan_actions: Codec = {"kind": "array", "items": () => codec_Action};
