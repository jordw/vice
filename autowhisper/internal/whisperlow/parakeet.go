// autowhisper/internal/whisperlow/parakeet.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package whisperlow

/*
#cgo LDFLAGS: ${SRCDIR}/../../../whisper.cpp/build_go/src/libparakeet.a
#include <parakeet.h>
#include <stdlib.h>

// Try/catch-wrapped init and inference, defined in whisper_safe.cpp.
struct parakeet_context* parakeet_safe_init(
    const char* path, bool use_gpu, int gpu_device,
    char* err_buf, size_t err_buf_size);
int parakeet_safe_full(
    struct parakeet_context* ctx, const float* samples, int n_samples, int n_threads,
    char* err_buf, size_t err_buf_size);
*/
import "C"

import (
	"errors"
	"strings"
	"unsafe"
)

// ParakeetContext is a loaded Parakeet model. It is not safe for concurrent use.
type ParakeetContext struct {
	ctx *C.struct_parakeet_context
}

// NewParakeetContext loads a ggml Parakeet model on the device that whisper
// models use.
func NewParakeetContext(path string) (*ParakeetContext, error) {
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	var errBuf [whisperInitErrBufSize]C.char
	ctx := C.parakeet_safe_init(p, C.bool(gpuEnabled), C.int(gpuDevice), &errBuf[0], C.size_t(len(errBuf)))
	if ctx == nil {
		return nil, errors.New(path + ": " + C.GoString(&errBuf[0]))
	}
	return &ParakeetContext{ctx: ctx}, nil
}

// Transcribe returns the text of 16 kHz mono samples.
func (c *ParakeetContext) Transcribe(samples []float32, threads int) (string, error) {
	if len(samples) == 0 {
		return "", nil
	}

	var errBuf [whisperInitErrBufSize]C.char
	if C.parakeet_safe_full(c.ctx, (*C.float)(unsafe.Pointer(&samples[0])), C.int(len(samples)), C.int(threads),
		&errBuf[0], C.size_t(len(errBuf))) != 0 {
		return "", errors.New("parakeet: " + C.GoString(&errBuf[0]))
	}
	var segs []string
	for i := range int(C.parakeet_full_n_segments(c.ctx)) {
		segs = append(segs, strings.TrimSpace(C.GoString(C.parakeet_full_get_segment_text(c.ctx, C.int(i)))))
	}
	return strings.Join(segs, " "), nil
}
