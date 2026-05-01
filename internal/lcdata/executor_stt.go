package lcdata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// whisperSem limits concurrent whisper-cpp subprocess invocations (CPU-bound).
var whisperSem = make(chan struct{}, 2)

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
	case "deepgram":
		return executeSTTDeepgram(ctx, node, audioURL, env, events)
	case "whisper", "openai-whisper":
		return executeSTTWhisper(ctx, node, audioURL, env, events)
	case "whisper-cpp", "whispercpp":
		return executeSTTWhisperCpp(ctx, node, audioURL, env, events)
	default:
		return nil, fmt.Errorf("unknown STT provider: %s (supported: deepgram, whisper, whisper-cpp)", node.Provider)
	}
}

func executeSTTDeepgram(
	ctx context.Context,
	node *Node,
	audioURL string,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {
	if env.DeepgramKey == "" {
		return nil, fmt.Errorf("deepgramKey not set in environment config (also checks DEEPGRAM_API_KEY)")
	}

	language := node.Language
	if language == "" {
		language = "en"
	}

	// Deepgram pre-recorded transcription via URL
	endpoint := fmt.Sprintf(
		"https://api.deepgram.com/v1/listen?model=nova-2&language=%s&smart_format=true&punctuate=true",
		language,
	)

	payload, _ := json.Marshal(map[string]string{"url": audioURL})
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to build deepgram request: %w", err)
	}
	req.Header.Set("Authorization", "Token "+env.DeepgramKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("deepgram request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read deepgram response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("deepgram returned status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Results struct {
			Channels []struct {
				Alternatives []struct {
					Transcript string  `json:"transcript"`
					Confidence float64 `json:"confidence"`
					Words      []struct {
						Word       string  `json:"word"`
						Start      float64 `json:"start"`
						End        float64 `json:"end"`
						Confidence float64 `json:"confidence"`
					} `json:"words"`
				} `json:"alternatives"`
			} `json:"channels"`
		} `json:"results"`
		Metadata struct {
			Duration float64 `json:"duration"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to decode deepgram response: %w", err)
	}

	if len(result.Results.Channels) == 0 || len(result.Results.Channels[0].Alternatives) == 0 {
		return nil, fmt.Errorf("deepgram returned no transcription results")
	}

	alt := result.Results.Channels[0].Alternatives[0]

	words := make([]any, len(alt.Words))
	for i, w := range alt.Words {
		words[i] = map[string]any{
			"word":       w.Word,
			"start":      w.Start,
			"end":        w.End,
			"confidence": w.Confidence,
		}
	}

	return map[string]any{
		"transcript": alt.Transcript,
		"confidence": alt.Confidence,
		"words":      words,
		"duration":   result.Metadata.Duration,
		"language":   language,
	}, nil
}

func executeSTTWhisper(
	ctx context.Context,
	node *Node,
	audioURL string,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {
	if env.OpenAIKey == "" {
		return nil, fmt.Errorf("openaiKey not set in environment config (required for whisper)")
	}

	// Download audio
	audioReq, err := http.NewRequestWithContext(ctx, "GET", audioURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build audio download request: %w", err)
	}
	audioResp, err := http.DefaultClient.Do(audioReq)
	if err != nil {
		return nil, fmt.Errorf("failed to download audio: %w", err)
	}
	defer audioResp.Body.Close()
	audioData, err := io.ReadAll(audioResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read audio data: %w", err)
	}

	// Build multipart form
	var buf bytes.Buffer
	boundary := "----lcdata-whisper-boundary"
	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString(`Content-Disposition: form-data; name="file"; filename="audio.mp3"` + "\r\n")
	buf.WriteString("Content-Type: audio/mpeg\r\n\r\n")
	buf.Write(audioData)
	buf.WriteString("\r\n--" + boundary + "\r\n")
	buf.WriteString(`Content-Disposition: form-data; name="model"` + "\r\n\r\nwhisper-1\r\n")
	if node.Language != "" {
		buf.WriteString("--" + boundary + "\r\n")
		buf.WriteString(`Content-Disposition: form-data; name="language"` + "\r\n\r\n" + node.Language + "\r\n")
	}
	buf.WriteString("--" + boundary + "--\r\n")

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/audio/transcriptions", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+env.OpenAIKey)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whisper request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Text  string `json:"text"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode whisper response: %w", err)
	}
	if result.Error != nil {
		return nil, fmt.Errorf("whisper error: %s", result.Error.Message)
	}

	return map[string]any{
		"transcript": result.Text,
		"confidence": 1.0,
		"words":      []any{},
	}, nil
}

func executeSTTWhisperCpp(
	ctx context.Context,
	node *Node,
	audioURL string,
	env EnvironmentConfig,
	events chan<- Event,
) (map[string]any, error) {
	bin := env.WhisperCppBin
	if bin == "" {
		bin = "whisper-cli"
	}

	model := node.Model
	if model == "" {
		model = env.WhisperCppModel
	}
	if model == "" {
		return nil, fmt.Errorf("whisper-cpp requires a model path: set node.model or whisperCppModel in env config")
	}

	// Resolve audio to a local file path — skip download if already local
	var audioFilePath string
	if strings.HasPrefix(audioURL, "/") || strings.HasPrefix(audioURL, "file://") {
		audioFilePath = strings.TrimPrefix(audioURL, "file://")
	} else {
		audioReq, err := http.NewRequestWithContext(ctx, "GET", audioURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to build audio download request: %w", err)
		}
		audioResp, err := http.DefaultClient.Do(audioReq)
		if err != nil {
			return nil, fmt.Errorf("failed to download audio: %w", err)
		}
		defer audioResp.Body.Close()
		audioData, err := io.ReadAll(audioResp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read audio data: %w", err)
		}

		ext := audioExtFromContentType(audioResp.Header.Get("Content-Type"))
		tmpAudio, err := os.CreateTemp("", "lcdata-whisper-*"+ext)
		if err != nil {
			return nil, fmt.Errorf("failed to create temp audio file: %w", err)
		}
		defer os.Remove(tmpAudio.Name())
		if _, err := tmpAudio.Write(audioData); err != nil {
			tmpAudio.Close()
			return nil, fmt.Errorf("failed to write temp audio file: %w", err)
		}
		tmpAudio.Close()
		audioFilePath = tmpAudio.Name()
	}

	// whisper-cli -otxt writes transcript to <audio_file>.txt
	txtPath := audioFilePath + ".txt"
	defer os.Remove(txtPath)

	lang := node.Language
	if lang == "" {
		lang = "en"
	}

	cmd := exec.CommandContext(ctx, bin,
		"-m", model,
		"-f", audioFilePath,
		"-l", lang,
		"-nt",   // no timestamps
		"-otxt", // write transcript to .txt file
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	whisperSem <- struct{}{}
	defer func() { <-whisperSem }()

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("whisper-cpp failed: %w\nstderr: %s", err, stderr.String())
	}

	txtData, err := os.ReadFile(txtPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read whisper-cpp transcript output: %w", err)
	}

	transcript := strings.TrimSpace(string(txtData))

	return map[string]any{
		"transcript": transcript,
		"confidence": 1.0,
		"words":      []any{},
		"language":   lang,
	}, nil
}

// audioExtFromContentType returns a file extension for a given audio Content-Type.
func audioExtFromContentType(ct string) string {
	ct = strings.ToLower(ct)
	switch {
	case strings.Contains(ct, "audio/wav"), strings.Contains(ct, "audio/wave"), strings.Contains(ct, "audio/x-wav"):
		return ".wav"
	case strings.Contains(ct, "audio/mpeg"), strings.Contains(ct, "audio/mp3"):
		return ".mp3"
	case strings.Contains(ct, "audio/ogg"):
		return ".ogg"
	case strings.Contains(ct, "audio/flac"), strings.Contains(ct, "audio/x-flac"):
		return ".flac"
	case strings.Contains(ct, "audio/mp4"), strings.Contains(ct, "audio/m4a"), strings.Contains(ct, "audio/x-m4a"):
		return ".m4a"
	case strings.Contains(ct, "audio/webm"):
		return ".webm"
	default:
		return ".wav"
	}
}
