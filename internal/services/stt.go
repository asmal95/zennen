package services

import (
	"context"

	openai "github.com/sashabaranov/go-openai"
)

// STTMinConfidence — порог средней avg_logprob сегментов, ниже которого
// расшифровку считаем сомнительной и просим подтверждения.
// Ориентиры Whisper: чистая речь ≈ −0.1…−0.4, шум ≈ −0.5…−1.0, мусор ≪ −1.0.
const STTMinConfidence = -0.8

func TranscribeFile(ctx context.Context, path, apiKey, baseURL, model string) string {
	text, _, _ := TranscribeDetailed(ctx, path, apiKey, baseURL, model)
	return text
}

// TranscribeDetailed возвращает текст и уверенность (средняя avg_logprob).
// Сначала пробует verbose_json (нужен для confidence), при ошибке — plain.
// conf == 0 означает «неизвестно» (фолбэк провайдера) — считать уверенным,
// как раньше.
func TranscribeDetailed(ctx context.Context, path, apiKey, baseURL, model string) (string, float64, error) {
	if apiKey == "" {
		return "", 0, nil
	}
	if model == "" {
		model = "openai/whisper-large-v3-turbo"
	}
	client := NewLLMClient(apiKey, baseURL)
	resp, err := client.CreateTranscription(ctx, openai.AudioRequest{
		Model:    model,
		FilePath: path,
		Language: "ru",
		Format:   openai.AudioResponseFormatVerboseJSON,
	})
	if err == nil {
		conf := 0.0
		if len(resp.Segments) > 0 {
			sum := 0.0
			for _, s := range resp.Segments {
				sum += s.AvgLogprob
			}
			conf = sum / float64(len(resp.Segments))
		}
		return resp.Text, conf, nil
	}
	resp2, err2 := client.CreateTranscription(ctx, openai.AudioRequest{
		Model:    model,
		FilePath: path,
		Language: "ru",
	})
	if err2 != nil {
		return "", 0, err2
	}
	return resp2.Text, 0, nil
}
