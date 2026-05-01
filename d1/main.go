package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gen2brain/malgo"
	mp3dec "github.com/hajimehoshi/go-mp3"
	"github.com/spf13/cobra"
)

// ── flags ───────────────────────────────────────────────────────────────────

var (
	flagBackend    string
	flagNode       string
	flagMic        int
	flagSpeaker    int
	flagToken      string
	flagListDev    bool
	flagSampleRate uint32
	flagEnv        string
)

// ── entry point ─────────────────────────────────────────────────────────────

func main() {
	root := &cobra.Command{
		Use:   "d1",
		Short: "Voice client for the lcdata AI backend",
		Long: `d1 captures audio from a microphone, sends it to an lcdata backend node,
and plays back the audio response. It works with any lcdata pipeline that
accepts audio_url input and returns audio_base64 output.

Typical flow:
  1. ./d1 --list-devices           (find your mic/speaker indices)
  2. ./d1 --mic 0 --speaker 1 --backend 192.168.1.10:8080 --node voice_assistant`,
		RunE: runClient,
	}

	f := root.Flags()
	f.StringVarP(&flagBackend, "backend", "b", "http://localhost:8080", "lcdata backend URL or host:port")
	f.StringVarP(&flagNode, "node", "n", "voice_agent_audio", "lcdata node/pipeline to call")
	f.IntVarP(&flagMic, "mic", "m", -1, "microphone device index (omit to select interactively)")
	f.IntVarP(&flagSpeaker, "speaker", "s", -1, "speaker device index (omit to select interactively)")
	f.StringVarP(&flagToken, "token", "t", "", "JWT bearer token (if backend requires auth)")
	f.BoolVarP(&flagListDev, "list-devices", "l", false, "list audio devices and exit")
	f.Uint32Var(&flagSampleRate, "sample-rate", 16000, "capture sample rate in Hz (16000 recommended for STT)")
	f.StringVarP(&flagEnv, "env", "e", "default", "lcdata environment name")

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// ── main client logic ────────────────────────────────────────────────────────

func runClient(cmd *cobra.Command, args []string) error {
	backend := normalizeURL(flagBackend)

	// Init miniaudio context
	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, func(string) {})
	if err != nil {
		return fmt.Errorf("audio context init failed: %w", err)
	}
	defer func() {
		_ = mctx.Uninit()
		mctx.Free()
	}()

	captures, err := mctx.Devices(malgo.Capture)
	if err != nil {
		return fmt.Errorf("enumerate capture devices: %w", err)
	}
	playbacks, err := mctx.Devices(malgo.Playback)
	if err != nil {
		return fmt.Errorf("enumerate playback devices: %w", err)
	}

	if flagListDev {
		printDeviceGroup("Capture  (microphones)", captures)
		fmt.Println()
		printDeviceGroup("Playback (speakers)   ", playbacks)
		return nil
	}

	micIdx := flagMic
	spkIdx := flagSpeaker

	if micIdx < 0 {
		printDeviceGroup("Capture devices (microphones)", captures)
		micIdx = pickDevice(len(captures), "Select microphone")
	}
	if spkIdx < 0 {
		printDeviceGroup("Playback devices (speakers)", playbacks)
		spkIdx = pickDevice(len(playbacks), "Select speaker")
	}
	if micIdx >= len(captures) {
		return fmt.Errorf("mic index %d out of range (0–%d)", micIdx, len(captures)-1)
	}
	if spkIdx >= len(playbacks) {
		return fmt.Errorf("speaker index %d out of range (0–%d)", spkIdx, len(playbacks)-1)
	}

	mic := captures[micIdx]
	spk := playbacks[spkIdx]

	// Header
	fmt.Println()
	logf("d1 — lcdata voice client")
	logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	logf("Microphone  : %s", mic.Name())
	logf("Speaker     : %s", spk.Name())
	logf("Backend     : %s", backend)
	logf("Node        : %s", flagNode)
	logf("Sample rate : %d Hz", flagSampleRate)
	logf("Environment : %s", flagEnv)
	logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Ping
	if err := pingBackend(backend, flagToken); err != nil {
		logf("⚠  Backend unreachable: %v", err)
		logf("   Continuing — it may come up before you speak.")
	} else {
		logf("✓  Backend reachable")
	}
	fmt.Println()
	logf("Press Enter to start recording.")
	logf("Press Enter again to stop and send.")
	logf("Type 'quit' or press Ctrl+C to exit.")
	fmt.Println()

	// Single goroutine owns stdin — sends lines on channel
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	// conversation history persists across turns
	var conversationHistory []any

	// ── interaction loop ─────────────────────────────────────────────────────
	for {
		fmt.Print("▶  ")
		line, ok := <-lines
		if !ok {
			break
		}
		if strings.EqualFold(strings.TrimSpace(line), "quit") {
			break
		}

		// ── record ────────────────────────────────────────────────────────────
		stopRec := make(chan struct{})
		var pcm []byte
		var recErr error
		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()
			pcm, recErr = captureAudio(mctx, mic, flagSampleRate, stopRec)
		}()

		logf("[REC] Recording… press Enter to stop")
		<-lines
		close(stopRec)
		wg.Wait()

		if recErr != nil {
			logf("[ERR] Recording failed: %v", recErr)
			continue
		}

		frames := len(pcm) / 2 // S16 = 2 bytes/sample
		dur := float64(frames) / float64(flagSampleRate)
		logf("[REC] Captured %.2fs  (%d KB)", dur, len(pcm)/1024)

		if dur < 0.25 {
			logf("[SKIP] Too short — try again")
			continue
		}

		// ── encode + send (streaming) ────────────────────────────────────────
		wav := encodeWAV(pcm, flagSampleRate, 1, 16)
		logf("[NET] Sending %.1f KB → %s  (node: %s)", float64(len(wav))/1024, backend, flagNode)

		t0 := time.Now()
		resp, err := streamAudio(backend, flagNode, flagToken, flagEnv, wav, conversationHistory)

		if err != nil {
			logf("[ERR] %v", err)
			continue
		}
		logf("[NET] Done in %.2fs", time.Since(t0).Seconds())

		// ── extract audio for playback ───────────────────────────────────────
		audio64, _, _, contentType, durationMS := extractOutput(resp)
		if durationMS > 0 {
			logf("[RUN] Total: %dms", durationMS)
		}

		// ── save history for next turn ───────────────────────────────────────
		if out, ok := resp["output"].(map[string]any); ok {
			if h, ok := out["history"].([]any); ok && len(h) > 0 {
				conversationHistory = h
			}
		}

		// ── play ─────────────────────────────────────────────────────────────
		if audio64 != "" {
			audioBytes, err := base64.StdEncoding.DecodeString(audio64)
			if err != nil {
				logf("[ERR] Base64 decode: %v", err)
			} else {
				logf("[PLAY] %.1f KB  content-type: %s  (press Enter to skip)", float64(len(audioBytes))/1024, contentType)
				stopPlay := make(chan struct{})
				playDone := make(chan error, 1)
				go func() {
					playDone <- playAudio(mctx, spk, audioBytes, contentType, flagSampleRate, stopPlay)
				}()
				select {
				case playErr := <-playDone:
					if playErr != nil {
						logf("[ERR] Playback: %v", playErr)
					} else {
						logf("[PLAY] Done")
					}
				case skippedLine, ok := <-lines:
					close(stopPlay)
					<-playDone // wait for goroutine to exit
					logf("[PLAY] Skipped")
					// if they typed quit while skipping, honour it
					if !ok || strings.EqualFold(strings.TrimSpace(skippedLine), "quit") {
						logf("Goodbye.")
						return nil
					}
				}
			}
		} else {
			logf("[INFO] No audio in response.")
		}

		fmt.Println()
	}

	logf("Goodbye.")
	return nil
}

// ── audio capture ────────────────────────────────────────────────────────────

// captureAudio records 16-bit mono PCM from dev until stop is closed.
func captureAudio(mctx *malgo.AllocatedContext, dev malgo.DeviceInfo, sampleRate uint32, stop <-chan struct{}) ([]byte, error) {
	var mu sync.Mutex
	var buf []byte

	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.DeviceID = dev.ID.Pointer()
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = 1
	cfg.SampleRate = sampleRate

	cb := malgo.DeviceCallbacks{
		Data: func(_, input []byte, _ uint32) {
			mu.Lock()
			buf = append(buf, input...)
			mu.Unlock()
		},
	}

	d, err := malgo.InitDevice(mctx.Context, cfg, cb)
	if err != nil {
		return nil, fmt.Errorf("init capture device: %w", err)
	}
	defer d.Uninit()

	if err := d.Start(); err != nil {
		return nil, fmt.Errorf("start capture: %w", err)
	}
	<-stop
	d.Stop()

	mu.Lock()
	out := make([]byte, len(buf))
	copy(out, buf)
	mu.Unlock()
	return out, nil
}

// ── audio playback ───────────────────────────────────────────────────────────

// playAudio decodes WAV or MP3 bytes and plays them on dev.
// Closes early if stop is closed.
func playAudio(mctx *malgo.AllocatedContext, dev malgo.DeviceInfo, data []byte, contentType string, fallbackRate uint32, stop <-chan struct{}) error {
	var pcm []byte
	var sampleRate uint32
	var channels int
	var audioFmt malgo.FormatType

	if isMP3(contentType, data) {
		raw, sr, err := decodeMP3(data)
		if err != nil {
			return fmt.Errorf("MP3 decode: %w", err)
		}
		pcm = raw
		sampleRate = sr
		channels = 2 // go-mp3 always outputs stereo
		audioFmt = malgo.FormatS16
	} else {
		raw, sr, ch, bps, err := decodeWAV(data)
		if err != nil {
			// Assume raw 16-bit PCM at fallback rate
			raw = data
			sr = fallbackRate
			ch = 1
			bps = 16
		}
		pcm = raw
		sampleRate = sr
		channels = ch
		switch bps {
		case 8:
			audioFmt = malgo.FormatU8
		case 32:
			audioFmt = malgo.FormatS32
		default:
			audioFmt = malgo.FormatS16
		}
	}

	var pos int
	var mu sync.Mutex
	var once sync.Once
	done := make(chan struct{})

	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.Playback.DeviceID = dev.ID.Pointer()
	cfg.Playback.Format = audioFmt
	cfg.Playback.Channels = uint32(channels)
	cfg.SampleRate = sampleRate

	bytesPerFrame := channels * 2 // S16 = 2 bytes; adjust if needed
	if audioFmt == malgo.FormatU8 {
		bytesPerFrame = channels * 1
	} else if audioFmt == malgo.FormatS32 {
		bytesPerFrame = channels * 4
	}

	cb := malgo.DeviceCallbacks{
		Data: func(output, _ []byte, frames uint32) {
			mu.Lock()
			defer mu.Unlock()
			need := int(frames) * bytesPerFrame
			if pos >= len(pcm) {
				once.Do(func() { close(done) })
				return
			}
			end := pos + need
			if end > len(pcm) {
				end = len(pcm)
			}
			copy(output, pcm[pos:end])
			pos = end
			if pos >= len(pcm) {
				once.Do(func() { close(done) })
			}
		},
	}

	d, err := malgo.InitDevice(mctx.Context, cfg, cb)
	if err != nil {
		return fmt.Errorf("init playback device: %w", err)
	}
	defer d.Uninit()

	if err := d.Start(); err != nil {
		return fmt.Errorf("start playback: %w", err)
	}
	defer d.Stop()

	select {
	case <-done:
		time.Sleep(300 * time.Millisecond) // drain hardware buffer
	case <-stop:
		// caller requested early stop
	case <-time.After(5 * time.Minute):
	}
	return nil
}

// ── HTTP ─────────────────────────────────────────────────────────────────────

// sendAudio POSTs a WAV file to /api/nodes/{node}/audio and returns the parsed Run JSON.
func sendAudio(backend, node, token, env string, wav []byte) (map[string]any, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	part, err := mw.CreateFormFile("audio", "recording.wav")
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(wav); err != nil {
		return nil, err
	}
	if env != "" {
		_ = mw.WriteField("env", env)
	}
	mw.Close()

	url := backend + "/api/nodes/" + node + "/audio"
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)

	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("JSON parse (status %d): %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if resp.StatusCode >= 400 {
		msg, _ := result["error"].(string)
		return nil, fmt.Errorf("backend %d: %s", resp.StatusCode, msg)
	}
	return result, nil
}

// streamAudio POSTs to /audio/stream and prints events in real time.
// Returns the completed Run JSON (same shape as sendAudio) for audio extraction.
func streamAudio(backend, node, token, env string, wav []byte, history []any) (map[string]any, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	part, err := mw.CreateFormFile("audio", "recording.wav")
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(wav); err != nil {
		return nil, err
	}
	if env != "" {
		_ = mw.WriteField("env", env)
	}
	if len(history) > 0 {
		histJSON, _ := json.Marshal(history)
		_ = mw.WriteField("history", string(histJSON))
	}
	mw.Close()

	url := backend + "/api/nodes/" + node + "/audio/stream"
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		var result map[string]any
		if json.Unmarshal(raw, &result) == nil {
			if msg, ok := result["error"].(string); ok {
				return nil, fmt.Errorf("backend %d: %s", resp.StatusCode, msg)
			}
		}
		return nil, fmt.Errorf("backend %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}

	type sEvent struct {
		Event      string         `json:"event"`
		RunID      string         `json:"run_id"`
		Node       string         `json:"node"`
		StepID     string         `json:"step_id"`
		Data       string         `json:"data"`
		Output     map[string]any `json:"output"`
		Error      string         `json:"error"`
		DurationMS int64          `json:"duration_ms"`
	}

	var finalRun map[string]any
	aiLineOpen := false

	// 32 MB buffer — audio_base64 in run_completed can be large
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 32<<20), 32<<20)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var ev sEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}

		switch ev.Event {
		case "step_started":
			if aiLineOpen {
				fmt.Println()
				aiLineOpen = false
			}
			logf("[STEP] %s  (%s)", ev.StepID, ev.Node)

		case "step_completed":
			if aiLineOpen {
				fmt.Println()
				aiLineOpen = false
			}
			// Show step error (on_error was triggered) as a warning
			if ev.Error != "" {
				logf("[WARN] %s: %s", ev.StepID, ev.Error)
			}
			if ev.Output != nil {
				if t, ok := ev.Output["transcript"].(string); ok && t != "" {
					logf("[YOU] %s", t)
				}
				// Only show AI text from LLM steps (response/text/answer keys).
				// Exclude "result" to avoid transform nodes printing as [AI].
				for _, key := range []string{"response", "text", "answer", "message"} {
					if s, ok := ev.Output[key].(string); ok && s != "" {
						logf("[AI ] %s", s)
						break
					}
				}
				// Show detected intent when the router step completes
				if intent, ok := ev.Output["intent"].(string); ok && intent != "" {
					// intent is JSON like {"intent":"delete_file","name":"test.txt"}
					// extract just the intent value for the log line
					var intentMap map[string]any
					if json.Unmarshal([]byte(intent), &intentMap) == nil {
						if iv, ok := intentMap["intent"].(string); ok {
							logf("[INTENT] %s", iv)
						}
					}
				}
			}
			if ev.DurationMS > 0 {
				logf("[DONE] %s  (%dms)", ev.StepID, ev.DurationMS)
			}

		case "chunk":
			if strings.HasPrefix(ev.Data, "[tool result:") {
				if aiLineOpen {
					fmt.Println()
					aiLineOpen = false
				}
				logf("[TOOL←] %s", strings.TrimPrefix(ev.Data, "[tool result: "))
			} else if strings.HasPrefix(ev.Data, "[tool:") {
				// Tool call notification — print on its own line
				if aiLineOpen {
					fmt.Println()
					aiLineOpen = false
				}
				logf("[TOOL→] %s", strings.TrimPrefix(ev.Data, "[tool: "))
			} else {
				// LLM streaming tokens (stream:true path)
				if !aiLineOpen {
					ts := time.Now().Format("15:04:05.000")
					fmt.Printf("[%s] [AI ] ", ts)
					aiLineOpen = true
				}
				fmt.Print(ev.Data)
			}

		case "run_completed":
			if aiLineOpen {
				fmt.Println()
				aiLineOpen = false
			}
			// The event JSON contains output at the top level
			var runMap map[string]any
			if err := json.Unmarshal(line, &runMap); err == nil {
				finalRun = runMap
			}

		case "run_failed":
			if aiLineOpen {
				fmt.Println()
				aiLineOpen = false
			}
			if ev.Error != "" {
				return nil, fmt.Errorf("backend: %s", ev.Error)
			}
			return nil, fmt.Errorf("backend run failed")
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("stream read: %w", err)
	}
	if finalRun == nil {
		return nil, fmt.Errorf("stream ended without completion event")
	}
	return finalRun, nil
}

func pingBackend(backend, token string) error {
	req, _ := http.NewRequest(http.MethodGet, backend+"/api/health", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// extractOutput pulls audio_base64, text response, transcript, content_type, and duration_ms
// from a Run response. Output may be nested under "output".
func extractOutput(run map[string]any) (audio64, text, transcript, contentType string, durationMS int64) {
	// duration_ms is at run level
	if d, ok := run["duration_ms"].(float64); ok {
		durationMS = int64(d)
	}

	searchIn := []map[string]any{run}
	if out, ok := run["output"].(map[string]any); ok {
		searchIn = append(searchIn, out)
	}
	// Also check error field
	if errMsg, ok := run["error"].(string); ok && errMsg != "" {
		text = "⚠ Backend error: " + errMsg
	}

	for _, m := range searchIn {
		if a, ok := m["audio_base64"].(string); ok && a != "" && audio64 == "" {
			audio64 = a
		}
		if ct, ok := m["content_type"].(string); ok && ct != "" && contentType == "" {
			contentType = ct
		}
		if t, ok := m["transcript"].(string); ok && t != "" && transcript == "" {
			transcript = t
		}
		if text == "" {
			for _, key := range []string{"response", "text", "result", "answer", "message"} {
				if s, ok := m[key].(string); ok && s != "" {
					text = s
					break
				}
			}
		}
	}
	if contentType == "" {
		contentType = "audio/wav"
	}
	return
}

// ── WAV ──────────────────────────────────────────────────────────────────────

func encodeWAV(pcm []byte, sampleRate uint32, channels, bitsPerSample uint16) []byte {
	dataLen := uint32(len(pcm))
	var b bytes.Buffer
	b.WriteString("RIFF")
	wU32(&b, 36+dataLen)
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	wU32(&b, 16)
	wU16(&b, 1) // PCM
	wU16(&b, channels)
	wU32(&b, sampleRate)
	wU32(&b, sampleRate*uint32(channels)*uint32(bitsPerSample)/8)
	wU16(&b, channels*bitsPerSample/8)
	wU16(&b, bitsPerSample)
	b.WriteString("data")
	wU32(&b, dataLen)
	b.Write(pcm)
	return b.Bytes()
}

func decodeWAV(data []byte) (pcm []byte, sampleRate uint32, channels, bitsPerSample int, err error) {
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, 0, 0, 0, fmt.Errorf("not a WAV file")
	}
	channels = int(binary.LittleEndian.Uint16(data[22:]))
	sampleRate = binary.LittleEndian.Uint32(data[24:])
	bitsPerSample = int(binary.LittleEndian.Uint16(data[34:]))
	offset := 12
	for offset+8 <= len(data) {
		chunkID := string(data[offset : offset+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[offset+4:]))
		if chunkID == "data" {
			end := offset + 8 + chunkSize
			if end > len(data) {
				end = len(data)
			}
			return data[offset+8 : end], sampleRate, channels, bitsPerSample, nil
		}
		offset += 8 + chunkSize
		if chunkSize%2 != 0 {
			offset++ // RIFF padding byte
		}
	}
	return nil, 0, 0, 0, fmt.Errorf("WAV data chunk not found")
}

func wU32(b *bytes.Buffer, v uint32) { binary.Write(b, binary.LittleEndian, v) } //nolint:errcheck
func wU16(b *bytes.Buffer, v uint16) { binary.Write(b, binary.LittleEndian, v) } //nolint:errcheck

// ── MP3 ──────────────────────────────────────────────────────────────────────

// isMP3 returns true when the content-type or magic bytes indicate MP3.
func isMP3(contentType string, data []byte) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "mpeg") || strings.Contains(ct, "mp3") {
		return true
	}
	// ID3 tag or MPEG sync word
	if len(data) >= 3 && (string(data[0:3]) == "ID3" || (data[0] == 0xFF && data[1]&0xE0 == 0xE0)) {
		return true
	}
	return false
}

// decodeMP3 decodes MP3 bytes to interleaved signed-16-bit PCM.
// go-mp3 always outputs stereo at the source sample rate.
func decodeMP3(data []byte) (pcm []byte, sampleRate uint32, err error) {
	d, err := mp3dec.NewDecoder(bytes.NewReader(data))
	if err != nil {
		return nil, 0, fmt.Errorf("mp3 decoder init: %w", err)
	}
	sampleRate = uint32(d.SampleRate())
	pcm, err = io.ReadAll(d)
	if err != nil {
		return nil, 0, fmt.Errorf("mp3 decode read: %w", err)
	}
	return pcm, sampleRate, nil
}

// ── UI helpers ───────────────────────────────────────────────────────────────

func logf(format string, args ...any) {
	ts := time.Now().Format("15:04:05.000")
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("[%s] %s\n", ts, msg)
}

func spinner(prefix string, stop <-chan struct{}) {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	i := 0
	for {
		select {
		case <-stop:
			fmt.Print("\r\033[K")
			return
		default:
			fmt.Printf("\r%s %s", prefix, frames[i%len(frames)])
			i++
			time.Sleep(80 * time.Millisecond)
		}
	}
}

func printDeviceGroup(label string, devices []malgo.DeviceInfo) {
	fmt.Printf("\n%s:\n", label)
	for i, d := range devices {
		fmt.Printf("  [%2d]  %s\n", i, d.Name())
	}
}

func pickDevice(count int, prompt string) int {
	r := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("%s [0–%d]: ", prompt, count-1)
		line, _ := r.ReadString('\n')
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && n >= 0 && n < count {
			return n
		}
		fmt.Printf("  Enter a number between 0 and %d.\n", count-1)
	}
}

func normalizeURL(s string) string {
	s = strings.TrimRight(s, "/")
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		s = "http://" + s
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

