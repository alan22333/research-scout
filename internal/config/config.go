package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	AppEnv                      string
	HTTPAddr                    string
	DatabaseURL                 string
	AutoMigrate                 bool
	DevAuthEnabled              bool
	DevActorID                  string
	DevWorkspaceID              string
	DevAllowActorHeader         bool
	ZoteroAPIBase               string
	ZoteroAPIVersion            string
	ZoteroRequestConcurrency    int
	S2APIKey                    string
	OpenAlexEmail               string
	LLMBaseURL                  string
	LLMAPIKey                   string
	LLMModel                    string
	EmbeddingBaseURL            string
	EmbeddingAPIKey             string
	EmbeddingModel              string
	EmbeddingDim                int
	EmbeddingNormalization      string
	EmbeddingQueryInstruction   string
	EmbeddingPassageInstruction string
	EmbeddingTruncationPolicy   string
	IndexEpochPublishMode       string
	PlannerVersion              string
	RetrievalConfigVersion      string
	PromptVersion               string
	RAGEvalDataset              string
	WorkerConcurrency           int
	QueryMaxSteps               int
	QuerySoftTimeoutSeconds     int
}

func envAny(names []string, fallback string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return fallback
}

func Load() (Config, error) {
	c := Config{
		AppEnv:                      env("APP_ENV", "local"),
		HTTPAddr:                    env("HTTP_ADDR", ":8080"),
		DatabaseURL:                 envAny([]string{"DATABASE_URL", "RS_DATABASE_URL"}, "postgres://research:research@localhost:55433/research_scout?sslmode=disable"),
		AutoMigrate:                 envBool("AUTO_MIGRATE", false),
		DevAuthEnabled:              envBool("DEV_AUTH_ENABLED", true),
		DevActorID:                  env("DEV_ACTOR_ID", "local-user"),
		DevWorkspaceID:              env("DEV_WORKSPACE_ID", "local-workspace"),
		DevAllowActorHeader:         envBool("DEV_ALLOW_ACTOR_HEADER", false),
		ZoteroAPIBase:               env("ZOTERO_API_BASE", "https://api.zotero.org"),
		ZoteroAPIVersion:            env("ZOTERO_API_VERSION", "3"),
		ZoteroRequestConcurrency:    envInt("ZOTERO_REQUEST_CONCURRENCY", 4),
		S2APIKey:                    os.Getenv("S2_API_KEY"),
		OpenAlexEmail:               os.Getenv("OPENALEX_EMAIL"),
		LLMBaseURL:                  os.Getenv("LLM_BASE_URL"),
		LLMAPIKey:                   os.Getenv("LLM_API_KEY"),
		LLMModel:                    env("LLM_MODEL", "qwen-plus"),
		EmbeddingBaseURL:            os.Getenv("EMBEDDING_BASE_URL"),
		EmbeddingAPIKey:             os.Getenv("EMBEDDING_API_KEY"),
		EmbeddingModel:              env("EMBEDDING_MODEL", "Qwen3-Embedding-8B"),
		EmbeddingDim:                envInt("EMBEDDING_DIM", 1024),
		EmbeddingNormalization:      env("EMBEDDING_NORMALIZATION_VERSION", "l2-v1"),
		EmbeddingQueryInstruction:   os.Getenv("EMBEDDING_QUERY_INSTRUCTION"),
		EmbeddingPassageInstruction: os.Getenv("EMBEDDING_PASSAGE_INSTRUCTION"),
		EmbeddingTruncationPolicy:   env("EMBEDDING_TRUNCATION_POLICY_VERSION", "truncate-v1"),
		IndexEpochPublishMode:       env("INDEX_EPOCH_PUBLISH_MODE", "atomic"),
		PlannerVersion:              env("PLANNER_VERSION", "planner-v1"),
		RetrievalConfigVersion:      env("RETRIEVAL_CONFIG_VERSION", "retrieval-v1"),
		PromptVersion:               env("PROMPT_VERSION", "brief-v1"),
		RAGEvalDataset:              env("RAG_EVAL_DATASET", "eval/golden/rag-golden-v1.yaml"),
		WorkerConcurrency:           envInt("WORKER_CONCURRENCY", 4),
		QueryMaxSteps:               envInt("QUERY_MAX_STEPS", 8),
		QuerySoftTimeoutSeconds:     envInt("QUERY_SOFT_TIMEOUT_SECONDS", 120),
	}
	if c.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if c.IndexEpochPublishMode != "atomic" {
		return Config{}, fmt.Errorf("INDEX_EPOCH_PUBLISH_MODE must be atomic in v1")
	}
	if c.EmbeddingDim <= 0 || c.EmbeddingDim > 16000 {
		return Config{}, fmt.Errorf("EMBEDDING_DIM out of range")
	}
	if c.AppEnv == "production" && c.DevAuthEnabled {
		return Config{}, fmt.Errorf("DEV_AUTH_ENABLED cannot be true in production")
	}
	return c, nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
