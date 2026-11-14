// server/sttreports.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mmp/vice/log"
	"github.com/mmp/vice/speech/stt"
)

// maxSTTReportWAVBytes is the size of a 60 second WAV file at 16 kHz with
// 16-bit samples. The store does not save longer transmissions.
const maxSTTReportWAVBytes = 44 + 60*16000*2

// STTReport is the JSON file that the server saves for each reported voice
// transmission. It has the fields of an STT command log entry, so the tools
// that read those entries also read reports.
type STTReport struct {
	stt.TestFile
	SessionID             string  `json:"session_id"`
	WhisperRealtimeFactor float64 `json:"whisper_realtime_factor"`
	ControllerRadioName   string  `json:"controller_radio_name"`
}

// sttReportStore saves reported voice transmissions for testing and training
// speech recognition. Each one is an STTReport JSON file and a WAV file of
// its audio with the same name, in a directory for its UTC day. The store
// stops saving reports when its files reach maxBytes in total.
type sttReportStore struct {
	dir      string
	maxBytes int64
	lg       *log.Logger

	mu    sync.Mutex
	bytes int64 // Size of the files in dir
	seq   int   // Makes file names unique
	full  bool  // Whether the store is full, so that it says so only once
}

func newSTTReportStore(dir string, maxBytes int64, lg *log.Logger) (*sttReportStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	s := &sttReportStore{dir: dir, maxBytes: maxBytes, lg: lg}
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		s.bytes += info.Size()
		return nil
	})
	if err != nil {
		return nil, err
	}

	lg.Infof("Saving STT reports in %s, which holds %d MB of its %d MB limit", dir, s.bytes>>20, maxBytes>>20)
	return s, nil
}

// Save saves a report and its audio, unless the store is full. It logs any
// problem.
func (s *sttReportStore) Save(args *STTLogArgs) {
	if len(args.WAV) > maxSTTReportWAVBytes ||
		(len(args.WAV) > 0 && (len(args.WAV) < 44 || !bytes.Equal(args.WAV[:4], []byte("RIFF")) ||
			!bytes.Equal(args.WAV[8:12], []byte("WAVE")))) {
		s.lg.Warnf("STT report: ignoring %d bytes of audio that are not a WAV file of 60 seconds or less", len(args.WAV))
		return
	}

	now := time.Now().UTC()
	report := STTReport{
		TestFile: stt.TestFile{
			Time:              now.Format(time.RFC3339Nano),
			Level:             "INFO",
			Msg:               "STT command",
			Transcript:        args.WhisperTranscript,
			WhisperPrompt:     args.WhisperPrompt,
			WhisperDurationMs: float64(args.WhisperDuration.Microseconds()) / 1000,
			AudioDurationMs:   float64(args.AudioDuration.Microseconds()) / 1000,
			Processor:         args.WhisperProcessor,
			WhisperModel:      args.WhisperModel,
			Callsign:          args.Callsign,
			Command:           args.Commands,
			STTAircraft:       args.AircraftContext,
			Logs:              args.STTDebugLogs,
		},
		SessionID:             args.SessionID,
		WhisperRealtimeFactor: args.WhisperRealtimeFactor,
		ControllerRadioName:   args.ControllerRadioName,
	}
	j, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		s.lg.Warnf("STT report: %v", err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	n := int64(len(j) + len(args.WAV))
	if s.bytes+n > s.maxBytes {
		if !s.full {
			s.lg.Warnf("STT report: not saving reports, since %s holds %d MB", s.dir, s.bytes>>20)
			s.full = true
		}
		return
	}

	dir := filepath.Join(s.dir, now.Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.lg.Warnf("STT report: %v", err)
		return
	}
	s.seq++
	base := filepath.Join(dir, fmt.Sprintf("%s-%d", now.Format("150405.000"), s.seq))

	// The WAV file goes first, so that the audio of any JSON file is complete.
	if len(args.WAV) > 0 {
		if err := os.WriteFile(base+".wav", args.WAV, 0o644); err != nil {
			s.lg.Warnf("STT report: %v", err)
			return
		}
	}
	if err := os.WriteFile(base+".json", j, 0o644); err != nil {
		s.lg.Warnf("STT report: %v", err)
		return
	}
	s.bytes += n
}
