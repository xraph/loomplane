import type { Codec } from '../codec-runtime';
import { codec_KubernetesRule_api_groups } from './KubernetesRule.api_groups';
import { codec_KubernetesRule_resources } from './KubernetesRule.resources';
import { codec_KubernetesRule_verbs } from './KubernetesRule.verbs';

export const codec_KubernetesRule: Codec = {"kind": "object", "fields": {"api_groups": {"ts": "apiGroups", "codec": () => codec_KubernetesRule_api_groups}, "resources": {"ts": "resources", "codec": () => codec_KubernetesRule_resources}, "verbs": {"ts": "verbs", "codec": () => codec_KubernetesRule_verbs}}, "required": ["api_groups", "resources", "verbs"]};
