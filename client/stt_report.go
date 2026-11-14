// client/stt_report.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package client

import (
	"crypto/rand"
	"encoding/binary"
	"math"

	"github.com/mmp/vice/server"
)

// sttSessionID groups the reports from one run of vice.
var sttSessionID = rand.Text()

// reportSTT sends a report of a transmission and its audio to the remote
// server, if the user allows reports and vice is connected to the remote
// server.
func (c *ControlClient) reportSTT(r *server.STTLogArgs, audio []float32) {
	c.mu.Lock()
	remote := c.remoteServer
	c.mu.Unlock()
	if remote == nil || !c.sttReportsEnabled() {
		return
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
