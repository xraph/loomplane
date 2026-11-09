import type { Codec } from '../codec-runtime';
import { codec_WorkspacePlacement } from './WorkspacePlacement';

export const codec_WorkspaceView_placements: Codec = {"kind": "array", "items": () => codec_WorkspacePlacement};
