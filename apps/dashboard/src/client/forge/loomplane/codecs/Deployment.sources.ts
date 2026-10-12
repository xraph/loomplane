import type { Codec } from '../codec-runtime';
import { codec_SourceArtifact } from './SourceArtifact';

export const codec_Deployment_sources: Codec = {"kind": "array", "items": () => codec_SourceArtifact};
