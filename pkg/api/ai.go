package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"chronicle-server/pkg/config"

	"github.com/go-chi/chi/v5"
)

type KeyStatus struct {
	Configured bool   `json:"configured"`
	State      string `json:"state"` // "ok", "invalid", "error", "unchecked"
	Message    string `json:"message"`
	CheckedAt  int64  `json:"checkedAt"`
}

type AIStatus struct {
	OpenAI    KeyStatus `json:"openai"`
	Anthropic KeyStatus `json:"anthropic"`
	Gemini    KeyStatus `json:"gemini"`
}

var (
	statusCache AIStatus
	statusMu    sync.RWMutex
)

func InitAI(cfg *config.Config) {
	revalidateAIKeys(cfg)

	// Hourly background revalidation
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			revalidateAIKeys(cfg)
		}
	}()
}

func probeOpenAI(apiKey string) KeyStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.openai.com/v1/models", nil)
	if err != nil {
		return KeyStatus{Configured: true, State: "error", Message: err.Error(), CheckedAt: time.Now().UnixMilli()}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return KeyStatus{Configured: true, State: "error", Message: err.Error(), CheckedAt: time.Now().UnixMilli()}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return KeyStatus{Configured: true, State: "invalid", Message: "OpenAI rejected the key (401).", CheckedAt: time.Now().UnixMilli()}
	}
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return KeyStatus{Configured: true, State: "error", Message: fmt.Sprintf("OpenAI %d: %s", resp.StatusCode, string(bodyBytes)), CheckedAt: time.Now().UnixMilli()}
	}

	return KeyStatus{Configured: true, State: "ok", CheckedAt: time.Now().UnixMilli()}
}

func probeAnthropic(apiKey string) KeyStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// Single-token request to validate key
	payload := map[string]interface{}{
		"model":      "claude-3-5-haiku-20241022",
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
	}
	bodyBytes, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return KeyStatus{Configured: true, State: "error", Message: err.Error(), CheckedAt: time.Now().UnixMilli()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return KeyStatus{Configured: true, State: "error", Message: err.Error(), CheckedAt: time.Now().UnixMilli()}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return KeyStatus{Configured: true, State: "invalid", Message: "Anthropic rejected the key (401).", CheckedAt: time.Now().UnixMilli()}
	}

	return KeyStatus{Configured: true, State: "ok", CheckedAt: time.Now().UnixMilli()}
}

func probeGemini(apiKey string) KeyStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	urlStr := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models?key=%s", url.QueryEscape(apiKey))
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return KeyStatus{Configured: true, State: "error", Message: err.Error(), CheckedAt: time.Now().UnixMilli()}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return KeyStatus{Configured: true, State: "error", Message: err.Error(), CheckedAt: time.Now().UnixMilli()}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusForbidden {
		return KeyStatus{Configured: true, State: "invalid", Message: fmt.Sprintf("Gemini rejected the key (%d).", resp.StatusCode), CheckedAt: time.Now().UnixMilli()}
	}
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return KeyStatus{Configured: true, State: "error", Message: fmt.Sprintf("Gemini %d: %s", resp.StatusCode, string(bodyBytes)), CheckedAt: time.Now().UnixMilli()}
	}

	return KeyStatus{Configured: true, State: "ok", CheckedAt: time.Now().UnixMilli()}
}

func revalidateAIKeys(cfg *config.Config) AIStatus {
	var oa, an, gm KeyStatus

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		if cfg.OpenAIKey != "" {
			oa = probeOpenAI(cfg.OpenAIKey)
		} else {
			oa = KeyStatus{Configured: false, State: "unchecked", CheckedAt: time.Now().UnixMilli()}
		}
	}()

	go func() {
		defer wg.Done()
		if cfg.AnthropicKey != "" {
			an = probeAnthropic(cfg.AnthropicKey)
		} else {
			an = KeyStatus{Configured: false, State: "unchecked", CheckedAt: time.Now().UnixMilli()}
		}
	}()

	go func() {
		defer wg.Done()
		if cfg.GeminiKey != "" {
			gm = probeGemini(cfg.GeminiKey)
		} else {
			gm = KeyStatus{Configured: false, State: "unchecked", CheckedAt: time.Now().UnixMilli()}
		}
	}()

	wg.Wait()

	statusMu.Lock()
	statusCache = AIStatus{
		OpenAI:    oa,
		Anthropic: an,
		Gemini:    gm,
	}
	statusMu.Unlock()

	// Print summary to console
	summarize := func(id string, s KeyStatus) string {
		if !s.Configured {
			return fmt.Sprintf("  %-10s  (not configured)", id)
		}
		if s.State == "ok" {
			return fmt.Sprintf("  %-10s  OK", id)
		}
		msg := s.Message
		if msg != "" {
			msg = " — " + msg
		}
		return fmt.Sprintf("  %-10s  %s%s", id, strings.ToUpper(s.State), msg)
	}

	fmt.Println("[ai] Key validation:")
	fmt.Println(summarize("openai", oa))
	fmt.Println(summarize("anthropic", an))
	fmt.Println(summarize("gemini", gm))

	return statusCache
}

type AiHandler struct {
	cfg      *config.Config
	database *sql.DB
}

func NewAiHandler(cfg *config.Config, database *sql.DB) *AiHandler {
	return &AiHandler{
		cfg:      cfg,
		database: database,
	}
}

func (h *AiHandler) Mount(r chi.Router) {
	r.Get("/config", h.GetConfig)
	r.Post("/config/revalidate", h.PostRevalidate)

	// Gate UI enabled flag
	r.Group(func(g chi.Router) {
		g.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !h.cfg.AIUIEnabled {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					json.NewEncoder(w).Encode(map[string]string{"error": "AI is disabled on this server (AI_UI=off)"})
					return
				}
				next.ServeHTTP(w, r)
			})
		})

		g.Post("/respond", h.PostRespond)
		g.Post("/grammar", h.PostGrammar)
		g.Post("/clarity", h.PostClarity)
		g.Post("/speak", h.PostSpeak)
	})
}

func (h *AiHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	statusMu.RLock()
	status := statusCache
	statusMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"providers": map[string]interface{}{
			"openai": map[string]interface{}{
				"configured": status.OpenAI.Configured,
				"valid":      status.OpenAI.State == "ok",
				"state":      status.OpenAI.State,
				"message":    status.OpenAI.Message,
				"checkedAt":  status.OpenAI.CheckedAt,
			},
			"anthropic": map[string]interface{}{
				"configured": status.Anthropic.Configured,
				"valid":      status.Anthropic.State == "ok",
				"state":      status.Anthropic.State,
				"message":    status.Anthropic.Message,
				"checkedAt":  status.Anthropic.CheckedAt,
			},
			"gemini": map[string]interface{}{
				"configured": status.Gemini.Configured,
				"valid":      status.Gemini.State == "ok",
				"state":      status.Gemini.State,
				"message":    status.Gemini.Message,
				"checkedAt":  status.Gemini.CheckedAt,
			},
		},
		"defaultModel": h.cfg.AIModel,
		"audioModel":   h.cfg.AudioModel,
		"audioVoice":   h.cfg.AudioVoice,
		"uiEnabled":    h.cfg.AIUIEnabled,
	})
}

func (h *AiHandler) PostRevalidate(w http.ResponseWriter, r *http.Request) {
	status := revalidateAIKeys(h.cfg)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

type RespondRequestBody struct {
	Provider   string                 `json:"provider"`
	Model      string                 `json:"model"`
	Input      string                 `json:"input"`
	MaxTokens  *int                   `json:"maxTokens,omitempty"`
	System     *string                `json:"system,omitempty"`
	JsonSchema map[string]interface{} `json:"jsonSchema,omitempty"`
}

func (h *AiHandler) PostRespond(w http.ResponseWriter, r *http.Request) {
	var body RespondRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": "Invalid request body"}})
		return
	}

	if body.Provider == "" || body.Model == "" || body.Input == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": "Invalid request body"}})
		return
	}

	var apiKey string
	var envVar string
	switch body.Provider {
	case "openai":
		apiKey = h.cfg.OpenAIKey
		envVar = "OPENAI_API_KEY"
	case "anthropic":
		apiKey = h.cfg.AnthropicKey
		envVar = "ANTHROPIC_API_KEY"
	case "gemini":
		apiKey = h.cfg.GeminiKey
		envVar = "GEMINI_API_KEY"
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": "Unsupported provider"}})
		return
	}

	if apiKey == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": fmt.Sprintf("%s key not configured on server. Set %s and restart.", body.Provider, envVar)}})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	if body.Provider == "openai" {
		reqBody := map[string]interface{}{
			"model": body.Model,
			"input": body.Input,
		}
		if body.JsonSchema != nil {
			reqBody["text"] = map[string]interface{}{
				"format": map[string]interface{}{
					"type":   "json_schema",
					"name":   "chronicle_fill",
					"strict": true,
					"schema": body.JsonSchema,
				},
			}
		}

		payload, _ := json.Marshal(reqBody)
		req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/responses", bytes.NewReader(payload))
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": err.Error()}})
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": err.Error()}})
			return
		}
		defer resp.Body.Close()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	if body.Provider == "anthropic" {
		maxT := 4096
		if body.MaxTokens != nil {
			maxT = *body.MaxTokens
		}

		reqBody := map[string]interface{}{
			"model":      body.Model,
			"max_tokens": maxT,
			"messages":   []map[string]interface{}{{"role": "user", "content": body.Input}},
		}
		if body.System != nil && *body.System != "" {
			reqBody["system"] = *body.System
		}
		if body.JsonSchema != nil {
			reqBody["tools"] = []map[string]interface{}{{
				"name":         "chronicle_fill",
				"description":  "Return the filled fields.",
				"input_schema": body.JsonSchema,
			}}
			reqBody["tool_choice"] = map[string]interface{}{
				"type": "tool",
				"name": "chronicle_fill",
			}
		}

		payload, _ := json.Marshal(reqBody)
		req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(payload))
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": err.Error()}})
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": err.Error()}})
			return
		}
		defer resp.Body.Close()

		var data map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": "Failed to parse Anthropic response"}})
			return
		}

		if resp.StatusCode != http.StatusOK {
			msg := "Anthropic request failed"
			if errMsg, ok := data["error"].(map[string]interface{})["message"].(string); ok {
				msg = errMsg
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": msg}})
			return
		}

		text := ""
		if content, ok := data["content"].([]interface{}); ok {
			var toolBlock map[string]interface{}
			var textBlocks []string
			for _, item := range content {
				if block, ok := item.(map[string]interface{}); ok {
					if block["type"] == "tool_use" {
						toolBlock = block
						break
					} else if block["type"] == "text" {
						if txt, ok := block["text"].(string); ok {
							textBlocks = append(textBlocks, txt)
						}
					}
				}
			}

			if toolBlock != nil {
				inputBytes, _ := json.Marshal(toolBlock["input"])
				text = string(inputBytes)
			} else {
				text = strings.Join(textBlocks, "\n\n")
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"output": []map[string]interface{}{{
				"type": "message",
				"content": []map[string]interface{}{{
					"type": "output_text",
					"text": text,
				}},
			}},
			"_raw": data,
		})
		return
	}

	if body.Provider == "gemini" {
		maxT := 4096
		if body.MaxTokens != nil {
			maxT = *body.MaxTokens
		}

		reqBody := map[string]interface{}{
			"contents": []map[string]interface{}{{
				"role": "user",
				"parts": []map[string]interface{}{{
					"text": body.Input,
				}},
			}},
			"generationConfig": map[string]interface{}{
				"maxOutputTokens": maxT,
			},
		}

		if body.System != nil && *body.System != "" {
			reqBody["systemInstruction"] = map[string]interface{}{
				"parts": []map[string]interface{}{{
					"text": *body.System,
				}},
			}
		}

		if body.JsonSchema != nil {
			reqBody["generationConfig"].(map[string]interface{})["responseMimeType"] = "application/json"
			reqBody["generationConfig"].(map[string]interface{})["responseSchema"] = body.JsonSchema
		}

		payload, _ := json.Marshal(reqBody)
		urlStr := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", url.PathEscape(body.Model), url.QueryEscape(apiKey))
		req, err := http.NewRequestWithContext(ctx, "POST", urlStr, bytes.NewReader(payload))
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": err.Error()}})
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": err.Error()}})
			return
		}
		defer resp.Body.Close()

		var data map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": "Failed to parse Gemini response"}})
			return
		}

		if resp.StatusCode != http.StatusOK {
			msg := "Gemini request failed"
			if errSection, ok := data["error"].(map[string]interface{}); ok {
				if errMsg, ok := errSection["message"].(string); ok {
					msg = errMsg
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": msg}})
			return
		}

		text := ""
		if candidates, ok := data["candidates"].([]interface{}); ok && len(candidates) > 0 {
			if firstCand, ok := candidates[0].(map[string]interface{}); ok {
				if content, ok := firstCand["content"].(map[string]interface{}); ok {
					if parts, ok := content["parts"].([]interface{}); ok {
						var textParts []string
						for _, p := range parts {
							if partMap, ok := p.(map[string]interface{}); ok {
								if txt, ok := partMap["text"].(string); ok {
									textParts = append(textParts, txt)
								}
							}
						}
						text = strings.Join(textParts, "")
					}
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"output": []map[string]interface{}{{
				"type": "message",
				"content": []map[string]interface{}{{
					"type": "output_text",
					"text": text,
				}},
			}},
			"_raw": data,
		})
		return
	}
}

type TextRequestBody struct {
	Text string `json:"text"`
}

func (h *AiHandler) PostGrammar(w http.ResponseWriter, r *http.Request) {
	var body TextRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": "text required"}})
		return
	}

	apiKey := h.cfg.GeminiKey
	if apiKey == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": "Gemini key not configured (GEMINI_API_KEY)."}})
		return
	}

	model := os.Getenv("GRAMMAR_AI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}

	system := "You are a meticulous copy editor for prose fiction. Find only GENUINE grammatical or usage errors: subject–verb disagreement, wrong verb tense, pronoun case, missing/doubled words, malformed or incomplete sentences (a clause with no verb), run-ons, and clearly wrong word choice (their/there, your/you’re). DO NOT flag stylistic choices: deliberate sentence fragments for effect, dialogue voice, informal usage inside dialogue, comma style, or anything a literary author would defend. When unsure, do not flag. Quote the smallest exact substring of the text that contains the error."

	responseSchema := map[string]interface{}{
		"type": "array",
		"items": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"quote":      map[string]string{"type": "string"},
				"message":    map[string]string{"type": "string"},
				"suggestion": map[string]string{"type": "string"},
			},
			"required": []string{"quote", "message"},
		},
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	gBody := map[string]interface{}{
		"contents": []map[string]interface{}{{
			"role": "user",
			"parts": []map[string]interface{}{{
				"text": body.Text,
			}},
		}},
		"systemInstruction": map[string]interface{}{
			"parts": []map[string]interface{}{{
				"text": system,
			}},
		},
		"generationConfig": map[string]interface{}{
			"responseMimeType": "application/json",
			"responseSchema":   responseSchema,
			"maxOutputTokens":  4096,
		},
	}

	payload, _ := json.Marshal(gBody)
	urlStr := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", url.PathEscape(model), url.QueryEscape(apiKey))
	req, err := http.NewRequestWithContext(ctx, "POST", urlStr, bytes.NewReader(payload))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": err.Error()}})
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": err.Error()}})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errData map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&errData)
		msg := "Gemini request failed"
		if errSection, ok := errData["error"].(map[string]interface{}); ok {
			if errMsg, ok := errSection["message"].(string); ok {
				msg = errMsg
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": msg}})
		return
	}

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": "Failed to parse response"}})
		return
	}

	rawText := ""
	if candidates, ok := data["candidates"].([]interface{}); ok && len(candidates) > 0 {
		if firstCand, ok := candidates[0].(map[string]interface{}); ok {
			if content, ok := firstCand["content"].(map[string]interface{}); ok {
				if parts, ok := content["parts"].([]interface{}); ok {
					var textParts []string
					for _, p := range parts {
						if partMap, ok := p.(map[string]interface{}); ok {
							if txt, ok := partMap["text"].(string); ok {
								textParts = append(textParts, txt)
							}
						}
					}
					rawText = strings.Join(textParts, "")
				}
			}
		}
	}

	var issues []map[string]interface{}
	_ = json.Unmarshal([]byte(rawText), &issues)

	// Clean up fields to only return quote, message, suggestion
	var cleanedIssues []map[string]interface{}
	for _, issue := range issues {
		if q, ok := issue["quote"].(string); ok && q != "" {
			msg, _ := issue["message"].(string)
			sug, _ := issue["suggestion"].(string)
			item := map[string]interface{}{
				"quote":   q,
				"message": msg,
			}
			if sug != "" {
				item["suggestion"] = sug
			}
			cleanedIssues = append(cleanedIssues, item)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"issues": cleanedIssues})
}

func (h *AiHandler) PostClarity(w http.ResponseWriter, r *http.Request) {
	var body TextRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": "text required"}})
		return
	}

	apiKey := h.cfg.GeminiKey
	if apiKey == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": "Gemini key not configured (GEMINI_API_KEY)."}})
		return
	}

	model := os.Getenv("GRAMMAR_AI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}

	system := "You are a careful first reader of prose fiction. FIRST, read the entire passage and internalize the author's style: their sentence rhythm, diction register, pacing, and deliberate quirks. That style is the baseline — judge every sentence against the author's own voice, not against generic writing rules. THEN flag only passages a reasonable reader would stumble on RELATIVE TO that baseline: sentences that are hard to parse, ambiguous pronoun references, tangled or overlong constructions the surrounding prose doesn't support, unclear who-is-doing-what, jarring register shifts that don't read intentional, or wording clunky enough to pull the reader out of the story. For each, quote the smallest exact substring of the text that contains the problem, and in the message explain WHY it may read unclear — name the confusion a reader would feel, and where relevant, relate it to the surrounding prose (e.g. \"the paragraph's short, clipped sentences make this 40-word chain hard to track\"). NEVER include a rewrite, corrected version, replacement wording, or any suggested text. The message must describe the problem only; the author does the writing. DO NOT flag deliberate style: fragments for effect, dialogue voice, rhythm choices, or anything a literary author would defend. When unsure, do not flag."

	responseSchema := map[string]interface{}{
		"type": "array",
		"items": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"quote":   map[string]string{"type": "string"},
				"message": map[string]string{"type": "string"},
			},
			"required": []string{"quote", "message"},
		},
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	gBody := map[string]interface{}{
		"contents": []map[string]interface{}{{
			"role": "user",
			"parts": []map[string]interface{}{{
				"text": body.Text,
			}},
		}},
		"systemInstruction": map[string]interface{}{
			"parts": []map[string]interface{}{{
				"text": system,
			}},
		},
		"generationConfig": map[string]interface{}{
			"responseMimeType": "application/json",
			"responseSchema":   responseSchema,
			"maxOutputTokens":  4096,
		},
	}

	payload, _ := json.Marshal(gBody)
	urlStr := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", url.PathEscape(model), url.QueryEscape(apiKey))
	req, err := http.NewRequestWithContext(ctx, "POST", urlStr, bytes.NewReader(payload))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": err.Error()}})
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": err.Error()}})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errData map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&errData)
		msg := "Gemini request failed"
		if errSection, ok := errData["error"].(map[string]interface{}); ok {
			if errMsg, ok := errSection["message"].(string); ok {
				msg = errMsg
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": msg}})
		return
	}

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": "Failed to parse response"}})
		return
	}

	rawText := ""
	if candidates, ok := data["candidates"].([]interface{}); ok && len(candidates) > 0 {
		if firstCand, ok := candidates[0].(map[string]interface{}); ok {
			if content, ok := firstCand["content"].(map[string]interface{}); ok {
				if parts, ok := content["parts"].([]interface{}); ok {
					var textParts []string
					for _, p := range parts {
						if partMap, ok := p.(map[string]interface{}); ok {
							if txt, ok := partMap["text"].(string); ok {
								textParts = append(textParts, txt)
							}
						}
					}
					rawText = strings.Join(textParts, "")
				}
			}
		}
	}

	var issues []map[string]interface{}
	_ = json.Unmarshal([]byte(rawText), &issues)

	// Clean up fields to only return quote, message (no suggestions!)
	var cleanedIssues []map[string]interface{}
	for _, issue := range issues {
		if q, ok := issue["quote"].(string); ok && q != "" {
			msg, _ := issue["message"].(string)
			item := map[string]interface{}{
				"quote":   q,
				"message": msg,
			}
			cleanedIssues = append(cleanedIssues, item)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"issues": cleanedIssues})
}

type TtsRequestBody struct {
	Model *string `json:"model,omitempty"`
	Voice *string `json:"voice,omitempty"`
	Text  string  `json:"text"`
}

func (h *AiHandler) PostSpeak(w http.ResponseWriter, r *http.Request) {
	var body TtsRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": "Invalid TTS body"}})
		return
	}

	apiKey := h.cfg.OpenAIKey
	if apiKey == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": "OPENAI_API_KEY not configured on server."}})
		return
	}

	model := h.cfg.AudioModel
	if body.Model != nil && *body.Model != "" {
		model = *body.Model
	}
	voice := h.cfg.AudioVoice
	if body.Voice != nil && *body.Voice != "" {
		voice = *body.Voice
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	reqBody := map[string]interface{}{
		"model":           model,
		"voice":           voice,
		"input":           body.Text,
		"response_format": "mp3",
	}
	payload, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/audio/speech", bytes.NewReader(payload))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": err.Error()}})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": err.Error()}})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		log.Printf("TTS upstream error: %d, %s", resp.StatusCode, string(bodyBytes))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": fmt.Sprintf("TTS failed (%d)", resp.StatusCode)}})
		return
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-store")
	if resp.ContentLength > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", resp.ContentLength))
	}
	io.Copy(w, resp.Body)
}
