package services

import (
	openai "github.com/sashabaranov/go-openai"
)

// NewLLMClient строит OpenAI-совместимый клиент.
// Дефолтный провайдер — OpenRouter (OPENAI_BASE_URL), слаги моделей с префиксом,
// например openai/gpt-4o-mini. Пустой baseURL = api.openai.com.
func NewLLMClient(apiKey, baseURL string) *openai.Client {
	cfg := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	return openai.NewClientWithConfig(cfg)
}
