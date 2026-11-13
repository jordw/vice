// client/stt_report.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"crypto/rand"
	"encoding/binary"
	"math"
	"strings"
	"sync"
	"time"

	whisper "github.com/mmp/vice/autowhisper"
	"github.com/mmp/vice/log"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/util"
)

// parakeetModelFiles are the Parakeet models in resources/models that run on
// each reported transmission, to compare them with whisper.
var parakeetModelFiles = []string{
	"ggml-parakeet-tdt-0.6b-v3-f16.bin",
	"ggml-parakeet-tdt-0.6b-v3-q8_0.bin",
}

// parakeetModel is a loaded Parakeet model and its name in reports.
type parakeetModel struct {
	name  string
	model *whisper.ParakeetModel
}

var (
	parakeetModels     []parakeetModel
	parakeetModelsDone = make(chan struct{})
	parakeetModelsOnce sync.Once

	// sttSessionID groups the reports from one run of vice.
	sttSessionID = rand.Text()
)

// loadParakeetModels starts loading the Parakeet models in the background,
// unless it already started.
func loadParakeetModels(lg *log.Logger) {
	parakeetModelsOnce.Do(func() {
		go func() {
			defer lg.CatchAndReportCrash()
			defer close(parakeetModelsDone)

			for _, file := range parakeetModelFiles {
				start := time.Now()
				m, err := whisper.LoadParakeetModel(util.GetResourcePath("models/" + file))
				if err != nil {
					lg.Errorf("Unable to load Parakeet model: %v", err)
					continue
				}
				// The first transcription includes one-time setup, which
				// would otherwise be in the first report's latency.
				if _, err := m.Transcribe(make([]float32, 16000)); err != nil {
					lg.Errorf("%s: %v", file, err)
					continue
				}
				parakeetModels = append(parakeetModels, parakeetModel{
					name:  strings.TrimSuffix(strings.TrimPrefix(file, "ggml-"), ".bin"),
					model: m,
				})
				lg.Infof("Loaded %s in %v", file, time.Since(start))
			}
		}()
	})
}

// reportSTT runs the Parakeet models on a transmission and sends the report
// to the remote server, if the user allows reports and vice is connected to
// the remote server.
func (c *ControlClient) reportSTT(lg *log.Logger, r *server.STTLogArgs, audio []float32) {
	c.mu.Lock()
	remote := c.remoteServer
	c.mu.Unlock()
	if remote == nil || !c.sttReportsEnabled() {
		return
	}

	loadParakeetModels(lg)
	<-parakeetModelsDone
	for _, m := range parakeetModels {
		start := time.Now()
		text, err := m.model.Transcribe(audio)
		alt := server.STTAlternate{Model: m.name, LatencyMs: time.Since(start).Milliseconds(), Transcript: text}
		if err != nil {
			alt.Error = err.Error()
		} else if text != "" {
			if decoded, err := c.sttTranscriber.DecodeTranscript(r.AircraftContext, text, r.ControllerRadioName); err != nil {
				alt.Command = "Error: " + err.Error()
			} else {
				alt.Callsign, alt.Command, _ = strings.Cut(decoded, " ")
			}
		}
		lg.Infof("%s transcription completed in %dms: %q -> %s %s", alt.Model, alt.LatencyMs, alt.Transcript,
			alt.Callsign, alt.Command)
		r.Alternates = append(r.Alternates, alt)
	}

	r.SessionID = sttSessionID
	r.WAV = encodeWAV(audio)
	remote.Go(server.ReportSTTLogRPC, r, nil, nil)
}

// encodeWAV returns 16 kHz mono samples as a 16-bit PCM WAV file.
func encodeWAV(audio []float32) []byte {
	le := binary.LittleEndian
	w := []byte("RIFF")
	w = le.AppendUint32(w, uint32(36+2*len(audio)))
	w = append(w, "WAVEfmt "...)
	w = le.AppendUint32(w, 16)    // fmt chunk size
	w = le.AppendUint16(w, 1)     // PCM
	w = le.AppendUint16(w, 1)     // mono
	w = le.AppendUint32(w, 16000) // sample rate
	w = le.AppendUint32(w, 32000) // bytes per second
	w = le.AppendUint16(w, 2)     // bytes per frame
	w = le.AppendUint16(w, 16)    // bits per sample
	w = append(w, "data"...)
	w = le.AppendUint32(w, uint32(2*len(audio)))
	for _, s := range audio {
		v := math.Round(float64(s) * 32768)
		w = le.AppendUint16(w, uint16(int16(max(-32768, min(32767, v)))))
	}
	return w
}
