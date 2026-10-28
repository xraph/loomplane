package qualify

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/models/registry"
)

// speechWAV is "Loomplane qualification check", 16 kHz mono, under five
// seconds. The speech probe passes when the transcript says
// "qualification".
//
//go:embed testdata/speech.wav
var speechWAV []byte

// probeTimeout bounds one probe request. The engine is already healthy, so
// this is time to answer, not to load.
const probeTimeout = 3 * time.Minute

// maxProbeBody is the most a probe reads of an answer.
const maxProbeBody = 4 << 20

// Probe checks a healthy engine the way a caller would use it, picked by
// the entry's primary task, and returns a one-line detail when it passes.
// client reaches the engine (the host is ignored).
func Probe(ctx context.Context, client *http.Client, e registry.Entry, c *Container) (string, error) {
	if len(e.Tasks) == 0 {
		return "", errors.New("the entry lists no tasks")
	}
	switch e.Tasks[0] {
	case registry.TaskChat, registry.TaskReasoning, registry.TaskCode, registry.TaskVision:
		return probeChat(ctx, client, c)
	case registry.TaskEmbedding:
		if e.Embedding == nil || e.Embedding.Dimensions <= 0 {
			return "", errors.New("the entry doesn't say how many dimensions its vectors have")
		}
		return probeEmbedding(ctx, client, c, e.Embedding.Dimensions)
	case registry.TaskRerank:
		return probeRerank(ctx, client, c)
	case registry.TaskSpeech:
		return probeSpeech(ctx, client, c)
	}
	return "", fmt.Errorf("no probe for the %s task", e.Tasks[0])
}

// post sends body to path with the engine's key and returns the response.
// The caller closes it.
func post(ctx context.Context, client *http.Client, c *Container, path, contentType string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://engine"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+c.Key)
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	if res.StatusCode != http.StatusOK {
		snippet, readErr := io.ReadAll(io.LimitReader(res.Body, 300))
		res.Body.Close()
		if readErr != nil {
			snippet = nil
		}
		return nil, fmt.Errorf("POST %s answered HTTP %d: %s", path, res.StatusCode, oneLine(string(snippet)))
	}
	return res, nil
}

// probeChat streams a short completion and passes on 200 with non-empty
// output. A reasoning model may spend all 16 tokens thinking, so reasoning
// output counts too.
func probeChat(ctx context.Context, client *http.Client, c *Container) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"model":          c.ServedName,
		"messages":       []map[string]string{{"role": "user", "content": "Reply with the single word: ready"}},
		"max_tokens":     16,
		"stream":         true,
		"stream_options": map[string]bool{"include_usage": true},
	})
	if err != nil {
		return "", err
	}
	started := time.Now()
	res, err := post(ctx, client, c, "/v1/chat/completions", "application/json", body)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	var (
		first, last time.Time
		text        strings.Builder
		chunks      int
		usage       int
	)
	scanner := bufio.NewScanner(io.LimitReader(res.Body, maxProbeBody))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if jErr := json.Unmarshal([]byte(data), &chunk); jErr != nil {
			return "", fmt.Errorf("the stream sent a chunk that isn't JSON: %s", oneLine(data))
		}
		if chunk.Usage != nil {
			usage = chunk.Usage.CompletionTokens
		}
		for _, choice := range chunk.Choices {
			piece := choice.Delta.Content + choice.Delta.ReasoningContent + choice.Delta.Reasoning
			if piece == "" {
				continue
			}
			now := time.Now()
			if first.IsZero() {
				first = now
			}
			last = now
			chunks++
			text.WriteString(piece)
		}
	}
	if sErr := scanner.Err(); sErr != nil {
		return "", fmt.Errorf("reading the stream: %w", sErr)
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", errors.New("the chat completion came back empty")
	}
	tokens := usage
	if tokens == 0 {
		tokens = chunks
	}
	span := last.Sub(first).Seconds()
	if span <= 0 {
		span = last.Sub(started).Seconds()
	}
	rate := 0.0
	if span > 0 {
		rate = float64(tokens) / span
	}
	return fmt.Sprintf("first token %d ms, %d tokens/s", first.Sub(started).Milliseconds(), int(math.Round(rate))), nil
}

// probeEmbedding embeds two inputs and passes when both vectors have the
// registry's dimensions.
func probeEmbedding(ctx context.Context, client *http.Client, c *Container, dimensions int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"model": c.ServedName,
		"input": []string{"Loomplane qualification check", "A GPU runs the model this week."},
	})
	if err != nil {
		return "", err
	}
	started := time.Now()
	res, err := post(ctx, client, c, "/v1/embeddings", "application/json", body)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if dErr := json.NewDecoder(io.LimitReader(res.Body, maxProbeBody)).Decode(&out); dErr != nil {
		return "", fmt.Errorf("the embeddings answer isn't JSON: %w", dErr)
	}
	if len(out.Data) != 2 {
		return "", fmt.Errorf("asked for 2 vectors, got %d", len(out.Data))
	}
	for i, d := range out.Data {
		if len(d.Embedding) != dimensions {
			return "", fmt.Errorf("vector %d has %d dimensions; the registry says %d", i+1, len(d.Embedding), dimensions)
		}
	}
	return fmt.Sprintf("2 vectors of %d dimensions in %d ms", dimensions, time.Since(started).Milliseconds()), nil
}

// probeRerank asks Text Embeddings Inference to rank two texts for a
// query, the first relevant and the second not, and passes when the
// relevant one ranks first.
func probeRerank(ctx context.Context, client *http.Client, c *Container) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	body, err := json.Marshal(map[string]any{
		"query": "What is the capital of France?",
		"texts": []string{"Paris is the capital and largest city of France.", "Bananas are a good source of potassium."},
	})
	if err != nil {
		return "", err
	}
	started := time.Now()
	res, err := post(ctx, client, c, "/rerank", "application/json", body)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var ranks []struct {
		Index int     `json:"index"`
		Score float64 `json:"score"`
	}
	if dErr := json.NewDecoder(io.LimitReader(res.Body, maxProbeBody)).Decode(&ranks); dErr != nil {
		return "", fmt.Errorf("the rerank answer isn't JSON: %w", dErr)
	}
	if len(ranks) != 2 {
		return "", fmt.Errorf("asked to rank 2 texts, got %d scores", len(ranks))
	}
	best := ranks[0]
	for _, r := range ranks[1:] {
		if r.Score > best.Score {
			best = r
		}
	}
	if best.Index != 0 {
		return "", errors.New("the irrelevant text ranked first")
	}
	return fmt.Sprintf("ranked the relevant text first in %d ms", time.Since(started).Milliseconds()), nil
}

// probeSpeech transcribes the check recording and passes when the text
// says "qualification", ignoring case.
func probeSpeech(ctx context.Context, client *http.Client, c *Container) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if wErr := form.WriteField("model", c.ServedName); wErr != nil {
		return "", wErr
	}
	file, err := form.CreateFormFile("file", "speech.wav")
	if err != nil {
		return "", err
	}
	if _, wErr := file.Write(speechWAV); wErr != nil {
		return "", wErr
	}
	if cErr := form.Close(); cErr != nil {
		return "", cErr
	}
	started := time.Now()
	res, err := post(ctx, client, c, "/v1/audio/transcriptions", form.FormDataContentType(), body.Bytes())
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct {
		Text string `json:"text"`
	}
	if dErr := json.NewDecoder(io.LimitReader(res.Body, maxProbeBody)).Decode(&out); dErr != nil {
		return "", fmt.Errorf("the transcription answer isn't JSON: %w", dErr)
	}
	if !strings.Contains(strings.ToLower(out.Text), "qualification") {
		return "", fmt.Errorf("the transcript doesn't say \"qualification\": %q", oneLine(out.Text))
	}
	return fmt.Sprintf("transcribed the check recording in %d ms", time.Since(started).Milliseconds()), nil
}

// oneLine squeezes whitespace and trims s to 200 bytes, for errors.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = strings.ToValidUTF8(s[:200], "") + "..."
	}
	return s
}
