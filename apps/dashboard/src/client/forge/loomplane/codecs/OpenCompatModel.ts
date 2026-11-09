import type { Codec } from '../codec-runtime';

export const codec_OpenCompatModel: Codec = {"kind": "object", "fields": {"context_window": {"ts": "contextWindow"}, "dimensions": {"ts": "dimensions"}, "embedding_per_million": {"ts": "embeddingPerMillion"}, "id": {"ts": "id"}, "input_per_million": {"ts": "inputPerMillion"}, "max_output": {"ts": "maxOutput"}, "output_per_million": {"ts": "outputPerMillion"}, "task": {"ts": "task"}}, "required": ["id", "input_per_million", "output_per_million", "task"]};
