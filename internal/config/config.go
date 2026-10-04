package config

import (
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	BotToken      string
	OpenAIKey     string
	OpenAIBaseURL string
	OpenAIModel   string
	STTModel      string
	DBPath        string
	TZ            string
	MorningHour   int
	EveningHour   int
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func Load() Config {
	_ = godotenv.Load()
	return Config{
		BotToken:      getenv("BOT_TOKEN", ""),
		OpenAIKey:     getenv("OPENAI_API_KEY", ""),
		OpenAIBaseURL: getenv("OPENAI_BASE_URL", "https://openrouter.ai/api/v1"),
		OpenAIModel:   getenv("OPENAI_MODEL", "openai/gpt-4o-mini"),
		STTModel:      getenv("OPENAI_STT_MODEL", "openai/whisper-large-v3-turbo"),
		DBPath:        getenv("DB_PATH", "data/diary.db"),
		TZ:            getenv("TZ", "Europe/Moscow"),
		MorningHour:   getenvInt("MORNING_HOUR", 9),
		EveningHour:   getenvInt("EVENING_HOUR", 21),
	}
}
