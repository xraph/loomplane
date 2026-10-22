import type { Codec } from '../codec-runtime';
import { codec_KubernetesRule } from './KubernetesRule';

export const codec_ResolvedComponent_kubernetes_rules: Codec = {"kind": "array", "items": () => codec_KubernetesRule};
