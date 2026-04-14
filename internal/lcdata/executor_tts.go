package lcdata

import (
	"context"
	"fmt"
	"strings"
)

func executeTTS(
	ctx context.Context,
	node *Node,
	inputs map[string]any,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {

	text := stringVal(inputs, "text")
	if text == "" {
		return nil, fmt.Errorf("input.text is required for tts nodes")
	}

	switch strings.ToLower(node.Provider) {
	case "elevenlabs":
		return executeTTSElevenLabs(ctx, node, text, env, events)
	case "openai":
		return executeTTSOpenAI(ctx, node, text, env, events)
	default:
		return nil, fmt.Errorf("unknown TTS provider: %s", node.Provider)
	}
}

func executeTTSElevenLabs(
	ctx context.Context,
	node *Node,
	text string,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {
	if env.ElevenLabsKey == "" {
		return nil, fmt.Errorf("elevenlabsKey not set in environment config")
	}
	// TODO: POST to https://api.elevenlabs.io/v1/text-to-speech/{voice_id}
	// Stream audio chunks back as EventChunk events with base64 audio data
	_ = ctx
	_ = text
	return nil, fmt.Errorf("elevenlabs TTS executor not yet implemented")
}

func executeTTSOpenAI(
	ctx context.Context,
	node *Node,
	text string,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {
	if env.OpenAIKey == "" {
		return nil, fmt.Errorf("openaiKey not set in environment config")
	}
	// TODO: POST to https://api.openai.com/v1/audio/speech
	_ = ctx
	_ = text
	return nil, fmt.Errorf("openai TTS executor not yet implemented")
}
