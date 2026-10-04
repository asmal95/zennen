package services

import (
	"context"

	openai "github.com/sashabaranov/go-openai"
)

// TranscribeFile отправляет аудио на OpenAI-совместимый /audio/transcriptions
// (дефолт — OpenRouter). Возвращает "" при любой ошибке — хендлер попросит текстом.
func TranscribeFile(ctx context.Context, path, apiKey, baseURL, model string) string {
	if apiKey == "" {
		return ""
	}
	if model == "" {
		model = "openai/whisper-large-v3-turbo"
	}
	client := NewLLMClient(apiKey, baseURL)
	resp, err := client.CreateTranscription(ctx, openai.AudioRequest{
		Model:    model,
		FilePath: path,
		Language: "ru",
	})
	if err != nil {
		return ""
	}
	return resp.Text
}
