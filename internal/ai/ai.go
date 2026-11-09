package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxHistoryItems = 20
	maxHistoryChars = 6000
	maxChunks       = 4
	chunkSize       = 1900
)

type HistoryItem struct {
	Role    string
	Content string
}

type Config struct {
	Host        string
	Model       string
	System      string
	Temperature float64
	Think       bool
}

type Engine struct {
	mu     sync.Mutex
	hist   map[uint64][]HistoryItem
	cfg    Config
	client *http.Client
}

func New(cfg Config) *Engine {
	return &Engine{
		hist:   make(map[uint64][]HistoryItem),
		cfg:    cfg,
		client: &http.Client{Timeout: 180 * time.Second},
	}
}

func (e *Engine) Update(cfg Config) {
	e.mu.Lock()
	e.cfg = cfg
	e.mu.Unlock()
}

func (e *Engine) Clear(channel uint64) {
	e.mu.Lock()
	delete(e.hist, channel)
	e.mu.Unlock()
}

func (e *Engine) ClearAll() {
	e.mu.Lock()
	e.hist = make(map[uint64][]HistoryItem)
	e.mu.Unlock()
}

func (e *Engine) snapshot(channel uint64) []HistoryItem {
	e.mu.Lock()
	defer e.mu.Unlock()
	items := e.hist[channel]
	out := make([]HistoryItem, len(items))
	copy(out, items)
	return out
}

func (e *Engine) push(channel uint64, role, content string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	q := e.hist[channel]
	q = append(q, HistoryItem{role, content})
	for len(q) > maxHistoryItems {
		q = q[1:]
	}
	for len(q) > 0 {
		total := 0
		for it := range q {
			total += len(q[it].Content)
		}
		if total <= maxHistoryChars {
			break
		}
		q = q[1:]
	}
	e.hist[channel] = q
}

type Status struct {
	Running bool
	Models  []string
}

func (e *Engine) Status() Status {
	cfg := e.snapshotConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.Host, "/")+"/api/tags", nil)
	if err != nil {
		return Status{}
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return Status{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Status{}
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return Status{}
	}
	st := Status{Running: true}
	for _, m := range tags.Models {
		st.Models = append(st.Models, m.Name)
	}
	return st
}

type generateRequest struct {
	Model   string         `json:"model"`
	System  string         `json:"system"`
	Prompt  string         `json:"prompt"`
	Stream  bool           `json:"stream"`
	Think   *bool          `json:"think,omitempty"`
	Options map[string]any `json:"options,omitempty"`
}

type generateResponse struct {
	Response string `json:"response"`
	Error    string `json:"error"`
}

func (e *Engine) call(url, model, system, prompt string, temperature float64, think bool) (string, error) {
	req := generateRequest{
		Model:  model,
		System: system,
		Prompt: prompt,
		Stream: false,
		Options: map[string]any{
			"temperature": temperature,
			"num_ctx":     4096,
		},
	}
	if think {
		t := true
		req.Think = &t
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequest(http.MethodPost, url+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("ollama %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var gen generateResponse
	if err := json.Unmarshal(data, &gen); err != nil {
		return "", fmt.Errorf("ollama decode: %w", err)
	}
	if gen.Error != "" {
		return "", fmt.Errorf("ollama: %s", gen.Error)
	}
	if strings.TrimSpace(gen.Response) == "" {
		return "", fmt.Errorf("ollama returned empty reply")
	}
	return gen.Response, nil
}

func supportsThinking(err string) bool {
	return strings.Contains(err, "does not support thinking") ||
		strings.Contains(err, "thinking is not supported")
}

func sanitizeReply(raw string) string {
	s := strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return ' '
		}
		return r
	}, raw)
	joined := strings.Join(strings.Fields(s), " ")
	for strings.HasPrefix(joined, "[") {
		idx := strings.Index(joined, "]")
		if idx <= 0 || idx > 64 {
			break
		}
		joined = strings.TrimSpace(joined[idx+1:])
	}
	return joined
}

func BuildTranscript(past []HistoryItem, speaker, prompt string) string {
	tagged := fmt.Sprintf("[%s]: %s", speaker, prompt)
	var b strings.Builder
	for _, item := range past {
		content := sanitizeReply(item.Content)
		if content == "" {
			continue
		}
		if item.Role == "user" {
			b.WriteString(content)
		} else {
			b.WriteString("[assistant]: " + content)
		}
		b.WriteString("\n")
	}
	b.WriteString(tagged)
	return b.String()
}

func SpeakerTag(name string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 {
			return ' '
		}
		return r
	}, name)
	clean = strings.Join(strings.Fields(clean), " ")
	if len([]rune(clean)) > 64 {
		clean = string([]rune(clean)[:64])
	}
	if strings.TrimSpace(clean) == "" {
		return "someone"
	}
	return clean
}

func ChunkReply(s string) []string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= chunkSize {
		return []string{s}
	}
	var chunks []string
	for i := 0; len(runes) > 0 && i < maxChunks; i++ {
		if i == maxChunks-1 && len(runes) > chunkSize {
			chunks = append(chunks, string(runes[:chunkSize]))
			break
		}
		n := chunkSize
		if n > len(runes) {
			n = len(runes)
		}
		chunks = append(chunks, string(runes[:n]))
		runes = runes[n:]
	}
	if len(runes) > 0 && len(chunks) == maxChunks {
		chunks = append(chunks[:maxChunks-1], string(runes[:chunkSize]))
	}
	return chunks
}

func (e *Engine) Ask(channel uint64, speaker, prompt string) (string, error) {
	cfg := e.snapshotConfig()
	url := strings.TrimRight(cfg.Host, "/")
	transcript := BuildTranscript(e.snapshot(channel), speaker, prompt)
	raw, err := e.call(url, cfg.Model, cfg.System, transcript, cfg.Temperature, cfg.Think)
	if err != nil && cfg.Think && supportsThinking(err.Error()) {
		raw, err = e.call(url, cfg.Model, cfg.System, transcript, cfg.Temperature, false)
	}
	if err != nil {
		return "", err
	}
	reply := sanitizeReply(raw)
	e.push(channel, "user", transcript)
	e.push(channel, "assistant", reply)
	return reply, nil
}

func (e *Engine) snapshotConfig() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}
