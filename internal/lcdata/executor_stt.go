package lcdata

import (
	"context"
	"fmt"
	"strings"
)

func executeSTT(
	ctx context.Context,
	node *Node,
	inputs map[string]any,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {

	audioURL := stringVal(inputs, "audio_url")
	if audioURL == "" {
		return nil, fmt.Errorf("input.audio_url is required for stt nodes")
	}

	switch strings.ToLower(node.Provider) {
	case "whisper":
		return executeSTTWhisper(ctx, node, audioURL, env, events)
	case "deepgram":
		return executeSTTDeepgram(ctx, node, audioURL, env, events)
	default:
		return nil, fmt.Errorf("unknown STT provider: %s", node.Provider)
	}
}

func executeSTTWhisper(
	ctx context.Context,
	node *Node,
	audioURL string,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {
	// TODO: download audio from audioURL, send to Whisper API or local Whisper instance
	// Whisper can run locally (whisper.cpp) or via OpenAI's transcription endpoint
	_ = ctx
	_ = audioURL
	_ = env
	return nil, fmt.Errorf("whisper STT executor not yet implemented")
}

func executeSTTDeepgram(
	ctx context.Context,
	node *Node,
	audioURL string,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {
	if env.DeepgramKey == "" {
		return nil, fmt.Errorf("deepgramKey not set in environment config")
	}
	// TODO: call Deepgram API with audioURL
	_ = ctx
	_ = audioURL
	return nil, fmt.Errorf("deepgram STT executor not yet implemented")
}
