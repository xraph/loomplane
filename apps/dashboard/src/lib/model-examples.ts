// What a service that links to a model writes to call it. The snippets
// read MODEL_URL and MODEL_API_KEY from the environment, so a key never
// appears in one, and the model's name is the one it is served as.
import type { ExampleTask } from "@/lib/models"

export type Snippet = { id: string; label: string; code: string }

const base = `$MODEL_URL`

/**
 * str is text as a string literal JSON, Python and JavaScript all read the
 * same way, so a served name with a quote or a backslash in it stays one
 * string.
 */
function str(text: string): string {
  return JSON.stringify(text)
}

/**
 * inSingleQuotes makes text safe inside a shell's single quotes, where a
 * single quote ends the string: each one becomes '\''.
 */
function inSingleQuotes(text: string): string {
  return text.replace(/'/g, `'\\''`)
}

function chat(model: string): Snippet[] {
  return [
    {
      id: "curl",
      label: "curl",
      code: `curl "${base}/v1/chat/completions" \\
  -H "Authorization: Bearer $MODEL_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": ${inSingleQuotes(str(model))},
    "messages": [{"role": "user", "content": "Say hello."}]
  }'`,
    },
    {
      id: "python",
      label: "Python",
      code: `import os
from openai import OpenAI

client = OpenAI(
    base_url=os.environ["MODEL_URL"] + "/v1",
    api_key=os.environ["MODEL_API_KEY"],
)

reply = client.chat.completions.create(
    model=${str(model)},
    messages=[{"role": "user", "content": "Say hello."}],
)
print(reply.choices[0].message.content)`,
    },
    {
      id: "javascript",
      label: "JavaScript",
      code: `import OpenAI from "openai"

const client = new OpenAI({
  baseURL: process.env.MODEL_URL + "/v1",
  apiKey: process.env.MODEL_API_KEY,
})

const reply = await client.chat.completions.create({
  model: ${str(model)},
  messages: [{ role: "user", content: "Say hello." }],
})
console.log(reply.choices[0].message.content)`,
    },
  ]
}

function embedding(model: string, prefix = ""): Snippet[] {
  const query = str(`${prefix}How do I reset my password?`)
  return [
    {
      id: "curl",
      label: "curl",
      code: `curl "${base}/v1/embeddings" \\
  -H "Authorization: Bearer $MODEL_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"model": ${inSingleQuotes(str(model))}, "input": [${inSingleQuotes(query)}]}'`,
    },
    {
      id: "python",
      label: "Python",
      code: `import os
from openai import OpenAI

client = OpenAI(
    base_url=os.environ["MODEL_URL"] + "/v1",
    api_key=os.environ["MODEL_API_KEY"],
)

result = client.embeddings.create(
    model=${str(model)},
    input=[${query}],
)
print(len(result.data[0].embedding))`,
    },
    {
      id: "javascript",
      label: "JavaScript",
      code: `import OpenAI from "openai"

const client = new OpenAI({
  baseURL: process.env.MODEL_URL + "/v1",
  apiKey: process.env.MODEL_API_KEY,
})

const result = await client.embeddings.create({
  model: ${str(model)},
  input: [${query}],
})
console.log(result.data[0].embedding.length)`,
    },
  ]
}

function rerank(): Snippet[] {
  return [
    {
      id: "curl",
      label: "curl",
      code: `curl "${base}/rerank" \\
  -H "Authorization: Bearer $MODEL_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "query": "How do I reset my password?",
    "texts": ["Open Settings and choose Security.", "Our office is closed on Monday."]
  }'`,
    },
    {
      id: "python",
      label: "Python",
      code: `import os
import requests

reply = requests.post(
    os.environ["MODEL_URL"] + "/rerank",
    headers={"Authorization": "Bearer " + os.environ["MODEL_API_KEY"]},
    json={
        "query": "How do I reset my password?",
        "texts": ["Open Settings and choose Security.", "Our office is closed on Monday."],
    },
)
print(reply.json())`,
    },
    {
      id: "javascript",
      label: "JavaScript",
      code: `const reply = await fetch(process.env.MODEL_URL + "/rerank", {
  method: "POST",
  headers: {
    Authorization: "Bearer " + process.env.MODEL_API_KEY,
    "Content-Type": "application/json",
  },
  body: JSON.stringify({
    query: "How do I reset my password?",
    texts: ["Open Settings and choose Security.", "Our office is closed on Monday."],
  }),
})
console.log(await reply.json())`,
    },
  ]
}

function speech(model: string): Snippet[] {
  return [
    {
      id: "curl",
      label: "curl",
      code: `curl "${base}/v1/audio/transcriptions" \\
  -H "Authorization: Bearer $MODEL_API_KEY" \\
  -F '${inSingleQuotes(`model=${model}`)}' \\
  -F file=@meeting.wav`,
    },
    {
      id: "python",
      label: "Python",
      code: `import os
from openai import OpenAI

client = OpenAI(
    base_url=os.environ["MODEL_URL"] + "/v1",
    api_key=os.environ["MODEL_API_KEY"],
)

with open("meeting.wav", "rb") as audio:
    text = client.audio.transcriptions.create(model=${str(model)}, file=audio)
print(text.text)`,
    },
    {
      id: "javascript",
      label: "JavaScript",
      code: `import fs from "node:fs"
import OpenAI from "openai"

const client = new OpenAI({
  baseURL: process.env.MODEL_URL + "/v1",
  apiKey: process.env.MODEL_API_KEY,
})

const text = await client.audio.transcriptions.create({
  model: ${str(model)},
  file: fs.createReadStream("meeting.wav"),
})
console.log(text.text)`,
    },
  ]
}

/** snippets are the examples for a task, each calling the model by name. */
export function snippets(
  task: ExampleTask,
  model: string,
  /** What an embedding model wants in front of each query. */
  queryPrefix = ""
): Snippet[] {
  switch (task) {
    case "embedding":
      return embedding(model, queryPrefix)
    case "rerank":
      return rerank()
    case "speech-to-text":
      return speech(model)
    default:
      return chat(model)
  }
}

export const taskTitles: Record<ExampleTask, string> = {
  chat: "Chat",
  embedding: "Embeddings",
  rerank: "Reranking",
  "speech-to-text": "Speech to text",
}
