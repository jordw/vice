// server/sttreports_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/speech/stt"
)

// testWAV returns a WAV file of n silent 16 kHz mono 16-bit samples.
func testWAV(n int) []byte {
	le := binary.LittleEndian
	w := le.AppendUint32([]byte("RIFF"), uint32(36+2*n))
	w = append(w, "WAVEfmt "...)
	w = le.AppendUint32(w, 16)
	w = le.AppendUint16(w, 1)
	w = le.AppendUint16(w, 1)
	w = le.AppendUint32(w, 16000)
	w = le.AppendUint32(w, 32000)
	w = le.AppendUint16(w, 2)
	w = le.AppendUint16(w, 16)
	w = append(w, "data"...)
	w = le.AppendUint32(w, uint32(2*n))
	return append(w, make([]byte, 2*n)...)
}

func TestSTTReportStore(t *testing.T) {
	dir := t.TempDir()
	lg := log.New(false, "warn", t.TempDir())

	s, err := newSTTReportStore(dir, 1<<20, lg)
	if err != nil {
		t.Fatal(err)
	}

	args := &STTLogArgs{
		SessionID:           "session",
		Callsign:            "AAL123",
		Commands:            "D50",
		WhisperTranscript:   "american one twenty three descend and maintain five thousand",
		WhisperModel:        "ggml-small.en-jlvatc-q5_0.bin",
		ControllerRadioName: "New York Approach",
		AircraftContext:     map[string]stt.Aircraft{"American 123": {Callsign: "AAL123"}},
		Alternates: []STTAlternate{{
			Model:      "parakeet-tdt-0.6b-v3-f16",
			LatencyMs:  57,
			Transcript: "American 123 descend and maintain 5000",
			Callsign:   "AAL123",
			Command:    "D50",
		}},
		WAV: testWAV(16000),
	}
	s.Save(args)

	saved := func() []string {
		paths, err := filepath.Glob(filepath.Join(dir, "*", "*.json"))
		if err != nil {
			t.Fatal(err)
		}
		return paths
	}
	paths := saved()
	if len(paths) != 1 {
		t.Fatalf("saved %d reports, expected 1", len(paths))
	}

	// The tools that read STT command log entries read reports, too.
	tf, err := stt.LoadTestFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if tf.Msg != "STT command" || tf.Transcript != args.WhisperTranscript || tf.Callsign != "AAL123" ||
		tf.Command != "D50" || tf.STTAircraft["American 123"].Callsign != "AAL123" {
		t.Errorf("saved STT command fields %+v do not match the report", tf)
	}

	b, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var r STTReport
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.SessionID != "session" || r.ControllerRadioName != "New York Approach" ||
		len(r.Alternates) != 1 || r.Alternates[0] != args.Alternates[0] {
		t.Errorf("saved report %+v does not match the report", r)
	}

	wav, err := os.ReadFile(strings.TrimSuffix(paths[0], ".json") + ".wav")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wav, args.WAV) {
		t.Errorf("saved %d bytes of audio, not the %d that were reported", len(wav), len(args.WAV))
	}

	// Audio that is not a WAV file, or that is over 60 seconds, is not saved.
	s.Save(&STTLogArgs{WAV: bytes.Repeat([]byte("x"), 100)})
	s.Save(&STTLogArgs{WAV: testWAV(61 * 16000)})
	if n := len(saved()); n != 1 {
		t.Errorf("saved %d reports after two with bad audio, expected 1", n)
	}

	// A new store counts the files already there toward its limit.
	full, err := newSTTReportStore(dir, s.bytes+1000, lg)
	if err != nil {
		t.Fatal(err)
	}
	if full.bytes != s.bytes {
		t.Errorf("new store counted %d bytes, expected %d", full.bytes, s.bytes)
	}
	full.Save(args)
	if n := len(saved()); n != 1 {
		t.Errorf("saved %d reports past the size limit, expected 1", n)
	}
}
