import type { Codec } from '../codec-runtime';
import { codec_ArtifactChoice } from './ArtifactChoice';

export const codec_Plan_artifacts: Codec = {"kind": "array", "items": () => codec_ArtifactChoice};
