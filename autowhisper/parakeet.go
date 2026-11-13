// autowhisper/parakeet.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package autowhisper

import (
	"runtime"
	"sync"

	"github.com/mmp/vice/autowhisper/internal/whisperlow"
)

// ParakeetModel is a loaded NVIDIA Parakeet speech recognition model. It
// takes no prompt.
type ParakeetModel struct {
	mu  sync.Mutex
	ctx *whisperlow.ParakeetContext
}

// LoadParakeetModel loads a ggml Parakeet model from a file, on the device
// that whisper models use.
func LoadParakeetModel(path string) (*ParakeetModel, error) {
	ctx, err := whisperlow.NewParakeetContext(path)
	if err != nil {
		return nil, err
	}
	return &ParakeetModel{ctx: ctx}, nil
}

// Transcribe returns the text of 16 kHz mono samples. It uses as many
// threads as whisper does.
func (m *ParakeetModel) Transcribe(audio []float32) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ctx.Transcribe(audio, runtime.NumCPU())
}
