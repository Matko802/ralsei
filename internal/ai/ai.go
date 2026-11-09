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
		client: &http.Client{Timeout: 300 * time.Second},
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

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model     string         `json:"model"`
	Messages  []chatMessage  `json:"messages"`
	Stream    bool           `json:"stream"`
	Think     *bool          `json:"think,omitempty"`
	KeepAlive string         `json:"keep_alive,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
}

type chatResponse struct {
	Message struct {
		Role     string `json:"role"`
		Content  string `json:"content"`
		Thinking string `json:"thinking"`
	} `json:"message"`
	Error string `json:"error"`
}

func buildChatBody(model, system string, history []HistoryItem, speaker, prompt string, temperature float64, think, stream bool) ([]byte, error) {
	msgs := make([]chatMessage, 0, len(history)+2)
	if strings.TrimSpace(system) != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: system})
	}
	for _, h := range history {
		role := h.Role
		if role != "assistant" {
			role = "user"
		}
		msgs = append(msgs, chatMessage{Role: role, Content: h.Content})
	}
	user := prompt
	if speaker != "" {
		user = speaker + ": " + prompt
	}
	msgs = append(msgs, chatMessage{Role: "user", Content: user})

	req := chatRequest{
		Model:     model,
		Messages:  msgs,
		Stream:    stream,
		KeepAlive: "30m",
		Options: map[string]any{
			"temperature": temperature,
			"num_ctx":     4096,
		},
	}
	if think {
		t := true
		req.Think = &t
	}
	return json.Marshal(req)
}

func (e *Engine) post(url string, body []byte) (*http.Response, error) {
	httpReq, err := http.NewRequest(http.MethodPost, url+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	return e.client.Do(httpReq)
}

func (e *Engine) call(url, model, system string, history []HistoryItem, speaker, prompt string, temperature float64, think bool) (string, error) {
	body, err := buildChatBody(model, system, history, speaker, prompt, temperature, think, false)
	if err != nil {
		return "", err
	}
	resp, err := e.post(url, body)
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
	var out chatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("ollama decode: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("ollama: %s", out.Error)
	}
	if strings.TrimSpace(out.Message.Content) == "" {
		return "", fmt.Errorf("ollama returned empty reply")
	}
	return out.Message.Content, nil
}

func (e *Engine) callStream(url, model, system string, history []HistoryItem, speaker, prompt string, temperature float64, think bool, onDelta func(string)) (string, error) {
	body, err := buildChatBody(model, system, history, speaker, prompt, temperature, think, true)
	if err != nil {
		return "", err
	}
	resp, err := e.post(url, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	dec := json.NewDecoder(resp.Body)
	var full strings.Builder
	for {
		var chunk chatResponse
		if err := dec.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("ollama stream: %w", err)
		}
		if chunk.Error != "" {
			return "", fmt.Errorf("ollama: %s", chunk.Error)
		}
		if chunk.Message.Content != "" {
			full.WriteString(chunk.Message.Content)
			if onDelta != nil {
				onDelta(full.String())
			}
		}
	}
	if strings.TrimSpace(full.String()) == "" {
		return "", fmt.Errorf("ollama returned empty reply")
	}
	return full.String(), nil
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
		n := chunkSize
		if n > len(runes) {
			n = len(runes)
		}
		chunks = append(chunks, string(runes[:n]))
		runes = runes[n:]
	}
	return chunks
}

func (e *Engine) Ask(channel uint64, speaker, prompt string) (string, error) {
	cfg := e.snapshotConfig()
	url := strings.TrimRight(cfg.Host, "/")
	history := e.snapshot(channel)
	raw, err := e.call(url, cfg.Model, cfg.System, history, speaker, prompt, cfg.Temperature, cfg.Think)
	if err != nil && cfg.Think && supportsThinking(err.Error()) {
		raw, err = e.call(url, cfg.Model, cfg.System, history, speaker, prompt, cfg.Temperature, false)
	}
	if err != nil {
		return "", err
	}
	reply := sanitizeReply(raw)
	e.push(channel, "user", speaker+": "+prompt)
	e.push(channel, "assistant", reply)
	return reply, nil
}

func (e *Engine) AskStream(channel uint64, speaker, prompt string, onDelta func(string)) (string, error) {
	cfg := e.snapshotConfig()
	url := strings.TrimRight(cfg.Host, "/")
	history := e.snapshot(channel)
	raw, err := e.callStream(url, cfg.Model, cfg.System, history, speaker, prompt, cfg.Temperature, cfg.Think, onDelta)
	if err != nil && cfg.Think && supportsThinking(err.Error()) {
		raw, err = e.callStream(url, cfg.Model, cfg.System, history, speaker, prompt, cfg.Temperature, false, onDelta)
	}
	if err != nil {
		return "", err
	}
	reply := sanitizeReply(raw)
	e.push(channel, "user", speaker+": "+prompt)
	e.push(channel, "assistant", reply)
	return reply, nil
}

func (e *Engine) Warm() error {
	cfg := e.snapshotConfig()
	body, err := buildChatBody(cfg.Model, "", nil, "", "hi", cfg.Temperature, false, false)
	if err != nil {
		return err
	}
	resp, err := e.post(strings.TrimRight(cfg.Host, "/"), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (e *Engine) Complete(system, prompt string, temperature float64) (string, error) {
	cfg := e.snapshotConfig()
	url := strings.TrimRight(cfg.Host, "/")
	raw, err := e.call(url, cfg.Model, system, nil, "", prompt, temperature, false)
	if err != nil {
		return "", err
	}
	return sanitizeReply(raw), nil
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

func (e *Engine) snapshotConfig() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}
