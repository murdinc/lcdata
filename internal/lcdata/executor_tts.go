package lcdata

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// piperSem limits concurrent piper subprocess invocations.
var piperSem = make(chan struct{}, 4)

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
	case "piper":
		return executeTTSPiper(ctx, node, text, env, events)
	case "macos", "say":
		return executeTTSMacOS(ctx, node, text, env, events)
	default:
		return nil, fmt.Errorf("unknown TTS provider: %s (supported: elevenlabs, openai, piper, macos)", node.Provider)
	}
}

func executeTTSMacOS(
	ctx context.Context,
	node *Node,
	text string,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {
	// voice_id maps to the -v argument (e.g. "Samantha", "Alex", "Karen")
	// Leave blank to use the system default voice.
	voice := node.VoiceID

	// say writes AIFF; afconvert turns it into 16-bit 22050Hz WAV
	aiffFile, err := os.CreateTemp("", "lcdata-say-*.aiff")
	if err != nil {
		return nil, fmt.Errorf("macos tts: failed to create temp aiff: %w", err)
	}
	aiffFile.Close()
	defer os.Remove(aiffFile.Name())

	wavFile, err := os.CreateTemp("", "lcdata-say-*.wav")
	if err != nil {
		return nil, fmt.Errorf("macos tts: failed to create temp wav: %w", err)
	}
	wavFile.Close()
	defer os.Remove(wavFile.Name())

	// Build say command
	sayArgs := []string{"-o", aiffFile.Name()}
	if voice != "" {
		sayArgs = append(sayArgs, "-v", voice)
	}
	sayArgs = append(sayArgs, text)

	sayCmd := exec.CommandContext(ctx, "say", sayArgs...)
	var sayStderr bytes.Buffer
	sayCmd.Stderr = &sayStderr
	if err := sayCmd.Run(); err != nil {
		return nil, fmt.Errorf("macos tts: say failed: %w\nstderr: %s", err, sayStderr.String())
	}

	// Convert AIFF → WAV (16-bit little-endian, 22050 Hz)
	afCmd := exec.CommandContext(ctx, "afconvert",
		aiffFile.Name(), wavFile.Name(),
		"-d", "LEI16@22050",
		"-f", "WAVE",
	)
	var afStderr bytes.Buffer
	afCmd.Stderr = &afStderr
	if err := afCmd.Run(); err != nil {
		return nil, fmt.Errorf("macos tts: afconvert failed: %w\nstderr: %s", err, afStderr.String())
	}

	audioData, err := os.ReadFile(wavFile.Name())
	if err != nil {
		return nil, fmt.Errorf("macos tts: failed to read wav output: %w", err)
	}
	if len(audioData) == 0 {
		return nil, fmt.Errorf("macos tts: produced no audio output")
	}

	return map[string]any{
		"audio_base64": base64.StdEncoding.EncodeToString(audioData),
		"content_type": "audio/wav",
		"size_bytes":   len(audioData),
	}, nil
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

func executeTTSPiper(
	ctx context.Context,
	node *Node,
	text string,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {
	bin := env.PiperBin
	if bin == "" {
		bin = "piper"
	}

	voiceModel := node.VoiceID
	if voiceModel == "" {
		return nil, fmt.Errorf("piper requires a voice model path in voice_id (e.g. /path/to/en_US-lessac-medium.onnx)")
	}

	// Piper writes WAV to an output file; use a temp file
	tmpOut, err := os.CreateTemp("", "lcdata-piper-*.wav")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp output file: %w", err)
	}
	tmpOut.Close()
	defer os.Remove(tmpOut.Name())

	cmd := exec.CommandContext(ctx, bin, "--model", voiceModel, "--output_file", tmpOut.Name())
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	piperSem <- struct{}{}
	defer func() { <-piperSem }()

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("piper failed: %w\nstderr: %s", err, stderr.String())
	}

	audioData, err := os.ReadFile(tmpOut.Name())
	if err != nil {
		return nil, fmt.Errorf("failed to read piper output: %w", err)
	}
	if len(audioData) == 0 {
		return nil, fmt.Errorf("piper produced no audio output")
	}

	encoded := base64.StdEncoding.EncodeToString(audioData)

	return map[string]any{
		"audio_base64": encoded,
		"content_type": "audio/wav",
		"size_bytes":   len(audioData),
	}, nil
}
