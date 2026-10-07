package service

import "time"

// ModelDiscoveryTimeout bounds a model-list lookup, including all retries.
// Shared by Ollama and OpenAI-compatible discovery.
const ModelDiscoveryTimeout = 5 * time.Second
