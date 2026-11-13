// client/stt_replay_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	whisper "github.com/mmp/vice/autowhisper"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/speech/stt"
	"github.com/mmp/vice/util"
)

// TestReplaySTTReports runs the voice transmissions that a server saved with
// -sttreports, in the directory VICE_STT_REPLAY_DIR and its subdirectories,
// through every Parakeet model in resources/models. For each one, it prints
// the results in the report and then each model's transcript and the command
// that vice decodes from it. It is skipped unless VICE_STT_REPLAY_DIR is set.
func TestReplaySTTReports(t *testing.T) {
	dir := os.Getenv("VICE_STT_REPLAY_DIR")
	if dir == "" {
		t.Skip("VICE_STT_REPLAY_DIR is not set")
	}

	db.InitDB()
	tr := stt.NewTranscriber(log.New(false, "warn", t.TempDir()))

	paths, _ := filepath.Glob("../resources/models/ggml-parakeet-*.bin")
	models := make(map[string]*whisper.ParakeetModel)
	for _, path := range paths {
		m, err := whisper.LoadParakeetModel(path)
		if err != nil {
			t.Fatal(err)
		}
		models[strings.TrimSuffix(filepath.Base(path), ".bin")] = m
	}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || filepath.Ext(path) != ".json" {
			return err
		}
		var r server.STTReport
		if b, err := os.ReadFile(path); err != nil {
			return err
		} else if err := json.Unmarshal(b, &r); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		audio, err := readWAV(strings.TrimSuffix(path, ".json") + ".wav")
		if err != nil {
			return err
		}

		fmt.Printf("=== %s\n", path)
		fmt.Printf("  %s %.0fms: %q -> %s %s\n", r.WhisperModel, r.WhisperDurationMs, r.Transcript, r.Callsign, r.Command)
		for _, a := range r.Alternates {
			fmt.Printf("  %s %dms: %q -> %s %s\n", a.Model, a.LatencyMs, a.Transcript, a.Callsign, a.Command)
		}
		for _, name := range util.SortedMapKeys(models) {
			start := time.Now()
			text, err := models[name].Transcribe(audio)
			if err != nil {
				return err
			}
			ms := time.Since(start).Milliseconds()
			cmd, err := tr.DecodeTranscript(r.STTAircraft, text, r.ControllerRadioName)
			if err != nil {
				cmd = "Error: " + err.Error()
			}
			fmt.Printf("  replay %s %dms: %q -> %s\n", name, ms, text, cmd)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// readWAV returns the samples of a WAV file written by encodeWAV.
func readWAV(path string) ([]float32, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 44 {
		return nil, fmt.Errorf("%s: too short for a WAV file", path)
	}
	b = b[44:]
	audio := make([]float32, len(b)/2)
	for i := range audio {
		audio[i] = float32(int16(binary.LittleEndian.Uint16(b[2*i:]))) / 32768
	}
	return audio, nil
}
