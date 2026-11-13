// whisper_safe.cpp: C++ wrappers around whisper.cpp entry points that
// translate C++ exceptions into a caller-supplied error buffer and an
// error return.
// Without these, a throw inside whisper.cpp surfaces as a "signal
// arrived during external code execution" fatal error in the Go
// runtime, which cannot be recovered via recover() and so leaves no
// trace in vice.slog or the public crash server.
//
// The error message is written into a caller-supplied buffer in the
// same call as the init, rather than stored in thread-local storage
// and fetched later, because the Go scheduler is free to reschedule a
// goroutine onto a different OS thread between cgo calls.

#include <parakeet.h>
#include <whisper.h>

#include <cstring>
#include <exception>

namespace {

void copy_err(char* err_buf, size_t err_buf_size, const char* msg) {
    if (err_buf == nullptr || err_buf_size == 0) return;
    size_t n = std::strlen(msg);
    if (n >= err_buf_size) n = err_buf_size - 1;
    std::memcpy(err_buf, msg, n);
    err_buf[n] = '\0';
}

}  // namespace

extern "C" {

struct whisper_context* whisper_safe_init_from_file_with_params(
    const char* path, struct whisper_context_params params,
    char* err_buf, size_t err_buf_size) {
    if (err_buf != nullptr && err_buf_size > 0) err_buf[0] = '\0';
    try {
        return whisper_init_from_file_with_params(path, params);
    } catch (const std::exception& e) {
        copy_err(err_buf, err_buf_size, e.what());
        return nullptr;
    } catch (...) {
        copy_err(err_buf, err_buf_size, "unknown C++ exception");
        return nullptr;
    }
}

// whisper.cpp's Vulkan backend throws std::runtime_error from inference when
// GPU buffer allocation fails (seen in the wild on low-memory IGPs). Returns
// the underlying whisper_full_with_params_ptr status on success, or -1 with
// the exception message written to err_buf on a throw.
int whisper_safe_full_ptr(
    struct whisper_context* ctx, struct whisper_full_params* params,
    const float* samples, int n_samples,
    char* err_buf, size_t err_buf_size) {
    if (err_buf != nullptr && err_buf_size > 0) err_buf[0] = '\0';
    try {
        return whisper_full_with_params_ptr(ctx, params, samples, n_samples);
    } catch (const std::exception& e) {
        copy_err(err_buf, err_buf_size, e.what());
        return -1;
    } catch (...) {
        copy_err(err_buf, err_buf_size, "unknown C++ exception");
        return -1;
    }
}

// The Vulkan backend also throws from whisper_free once the device has been
// lost (e.g., after a driver reset), since tearing it down waits on the
// device. The rest of the context is leaked in that case. Returns 0 on
// success, or -1 with the exception message written to err_buf.
int whisper_safe_free(
    struct whisper_context* ctx, char* err_buf, size_t err_buf_size) {
    if (err_buf != nullptr && err_buf_size > 0) err_buf[0] = '\0';
    try {
        whisper_free(ctx);
        return 0;
    } catch (const std::exception& e) {
        copy_err(err_buf, err_buf_size, e.what());
        return -1;
    } catch (...) {
        copy_err(err_buf, err_buf_size, "unknown C++ exception");
        return -1;
    }
}

// Loads a Parakeet model on the given GPU, or on the CPU if use_gpu is false.
// Returns nullptr with a message in err_buf on failure.
struct parakeet_context* parakeet_safe_init(
    const char* path, bool use_gpu, int gpu_device,
    char* err_buf, size_t err_buf_size) {
    if (err_buf != nullptr && err_buf_size > 0) err_buf[0] = '\0';
    try {
        struct parakeet_context_params params = parakeet_context_default_params();
        params.use_gpu = use_gpu;
        params.gpu_device = gpu_device;
        struct parakeet_context* ctx = parakeet_init_from_file_with_params(path, params);
        if (ctx == nullptr) {
            copy_err(err_buf, err_buf_size, "failed to load model");
        }
        return ctx;
    } catch (const std::exception& e) {
        copy_err(err_buf, err_buf_size, e.what());
        return nullptr;
    } catch (...) {
        copy_err(err_buf, err_buf_size, "unknown C++ exception");
        return nullptr;
    }
}

// Transcribes 16 kHz mono samples with greedy decoding. Returns 0 on success,
// or -1 with a message in err_buf if the model failed or threw.
int parakeet_safe_full(
    struct parakeet_context* ctx, const float* samples, int n_samples, int n_threads,
    char* err_buf, size_t err_buf_size) {
    if (err_buf != nullptr && err_buf_size > 0) err_buf[0] = '\0';
    try {
        struct parakeet_full_params params = parakeet_full_default_params(PARAKEET_SAMPLING_GREEDY);
        params.n_threads = n_threads;
        if (parakeet_full(ctx, params, samples, n_samples) != 0) {
            copy_err(err_buf, err_buf_size, "inference failed");
            return -1;
        }
        return 0;
    } catch (const std::exception& e) {
        copy_err(err_buf, err_buf_size, e.what());
        return -1;
    } catch (...) {
        copy_err(err_buf, err_buf_size, "unknown C++ exception");
        return -1;
    }
}

}  // extern "C"
