package workingmemory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
)

// JournalCommitter appends each commit batch as one JSON line to a file and syncs it. It is the
// interim commit target until Node 1's write endpoint (Task 31) exists.
type JournalCommitter struct {
	Path string
	mu   sync.Mutex
}

// Commit appends the batch to the journal.
func (j *JournalCommitter) Commit(_ context.Context, batch CommitBatch) error {
	line, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("encode commit batch: %w", err)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	f, err := os.OpenFile(j.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("open commit journal: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("write commit journal: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync commit journal: %w", err)
	}
	return f.Close()
}

// EmbeddingScorer scores text against a task the way Node 3 does: cosine similarity of their
// embeddings, rounded to 4 decimal places. It calls an OpenAI-compatible /v1/embeddings endpoint.
type EmbeddingScorer struct {
	BaseURL string
	Model   string
	HTTP    *http.Client
}

type embeddingRequest struct {
	Model string   `json:"model,omitempty"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

// Score returns the task score, or nil and "embedder_unavailable" if the embedder fails.
func (e *EmbeddingScorer) Score(ctx context.Context, task, text string) (*float64, string) {
	vecs, err := e.embed(ctx, []string{task, text})
	if err != nil {
		return nil, "embedder_unavailable"
	}
	v := math.Round(cosine(vecs[0], vecs[1])*10000) / 10000
	return &v, "scored"
}

func (e *EmbeddingScorer) embed(ctx context.Context, input []string) ([][]float64, error) {
	body, err := json.Marshal(embeddingRequest{Model: e.Model, Input: input})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.BaseURL, "/")+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := e.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings returned %d", resp.StatusCode)
	}
	var er embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, err
	}
	out := make([][]float64, len(input))
	for _, d := range er.Data {
		if d.Index >= 0 && d.Index < len(out) {
			out[d.Index] = d.Embedding
		}
	}
	for _, v := range out {
		if len(v) == 0 {
			return nil, errors.New("embeddings response is missing a vector")
		}
	}
	return out, nil
}

func cosine(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
