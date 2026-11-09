import type { Codec } from '../codec-runtime';
import { codec_WorkspaceRegion } from './WorkspaceRegion';

export const codec_WorkspaceView_regions: Codec = {"kind": "array", "items": () => codec_WorkspaceRegion};
