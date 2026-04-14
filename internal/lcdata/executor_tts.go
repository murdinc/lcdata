package lcdata

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
		return nil, fmt.Errorf("unknown TTS provider: %s (supported: elevenlabs, openai)", node.Provider)
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
		return nil, fmt.Errorf("elevenlabsKey not set in environment config (also checks ELEVENLABS_API_KEY)")
	}

	voiceID := node.VoiceID
	if voiceID == "" {
		voiceID = "21m00Tcm4TlvDq8ikWAM" // default: Rachel
	}

	model := node.Model
	if model == "" {
		model = "eleven_multilingual_v2"
	}

	payload, _ := json.Marshal(map[string]any{
		"text":     text,
		"model_id": model,
		"voice_settings": map[string]any{
			"stability":        0.5,
			"similarity_boost": 0.75,
		},
	})

	endpoint := fmt.Sprintf("https://api.elevenlabs.io/v1/text-to-speech/%s", voiceID)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to build elevenlabs request: %w", err)
	}
	req.Header.Set("xi-api-key", env.ElevenLabsKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/mpeg")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("elevenlabs returned status %d: %s", resp.StatusCode, string(body))
	}

	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read elevenlabs audio: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(audioData)

	return map[string]any{
		"audio_base64": encoded,
		"content_type": "audio/mpeg",
		"size_bytes":   len(audioData),
	}, nil
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

	voice := node.VoiceID
	if voice == "" {
		voice = "alloy"
	}
	model := node.Model
	if model == "" {
		model = "tts-1"
	}

	payload, _ := json.Marshal(map[string]any{
		"model": model,
		"input": text,
		"voice": voice,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/audio/speech", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+env.OpenAIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai tts request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai tts returned status %d: %s", resp.StatusCode, string(body))
	}

	audioData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read openai tts audio: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(audioData)

	return map[string]any{
		"audio_base64": encoded,
		"content_type": "audio/mpeg",
		"size_bytes":   len(audioData),
	}, nil
}
