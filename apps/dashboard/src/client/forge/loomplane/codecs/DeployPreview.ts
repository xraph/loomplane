import type { Codec } from '../codec-runtime';
import { codec_Cost } from './Cost';
import { codec_DeployPreview_environments } from './DeployPreview.environments';
import { codec_DeployPreview_problems } from './DeployPreview.problems';
import { codec_EntrySummary } from './EntrySummary';
import { codec_Fit } from './Fit';
import { codec_GPURequest } from './GPURequest';
import { codec_Operation } from './Operation';
import { codec_TargetChoice } from './TargetChoice';

export const codec_DeployPreview: Codec = {"kind": "object", "fields": {"adds_to": {"ts": "addsTo"}, "can_deploy": {"ts": "canDeploy"}, "cost": {"ts": "cost", "codec": () => codec_Cost}, "entry": {"ts": "entry", "codec": () => codec_EntrySummary}, "environments": {"ts": "environments", "codec": () => codec_DeployPreview_environments}, "fit": {"ts": "fit", "codec": () => codec_Fit}, "gated": {"ts": "gated"}, "gpu": {"ts": "gpu", "codec": () => codec_GPURequest}, "kind": {"ts": "kind"}, "max_model_len": {"ts": "maxModelLen"}, "problems": {"ts": "problems", "codec": () => codec_DeployPreview_problems}, "quantization": {"ts": "quantization"}, "resource": {"ts": "resource"}, "server": {"ts": "server", "codec": () => codec_Operation}, "server_review_needed": {"ts": "serverReviewNeeded"}, "target": {"ts": "target", "codec": () => codec_TargetChoice}, "target_name": {"ts": "targetName"}, "token_ready": {"ts": "tokenReady"}, "version": {"ts": "version"}}, "required": ["can_deploy", "cost", "entry", "environments", "fit", "gated", "kind", "max_model_len", "problems", "quantization", "resource", "target", "target_name", "token_ready", "version"]};
