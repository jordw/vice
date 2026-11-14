// cmd/sttmodels/main.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// cmd/sttmodels compares speech recognition models on the voice
// transmissions that vice users reported, which viceserver saves with
// -sttreports as a JSON file and a WAV file for each transmission. It runs
// each model on each transmission, decodes each transcript with speech/stt
// and the aircraft context in the report, and prints how often each model
// gives the correct command.
//
// Usage:
//
//	sttmodels [flags] reports-directory
//
// Each model transcribes a transmission once. The transcripts are cached,
// so a later run transcribes only the new transmissions, but every run
// decodes all of them with the current decoder. The correct command for a
// transmission is its label: -label plays each unlabeled transmission and
// asks for it. The cache and the labels identify a transmission by its path
// in the reports directory, so give the same directory each time.
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	whisper "github.com/mmp/vice/autowhisper"
	"github.com/mmp/vice/aviation/db"
	"github.com/mmp/vice/server"
	"github.com/mmp/vice/speech/stt"
	"github.com/mmp/vice/util"
)

var (
	modelsFlag = flag.String("models", "resources/models/ggml-*.bin",
		"comma-separated model files or glob patterns. Files with \"parakeet\" in their names are Parakeet models, the others whisper models")
	cacheFlag  = flag.String("cache", "~/.sttmodels/transcripts.jsonl", "`file` of cached transcripts")
	labelsFlag = flag.String("labels", "~/.sttmodels/labels.json", "`file` of the correct commands")
	labelFlag  = flag.Bool("label", false, "play the unlabeled transmissions and ask for their correct commands")
	listFlag   = flag.Bool("list", false, "print the transcripts and commands of each transmission")
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: sttmodels [flags] reports-directory\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(flag.Arg(0)); err != nil {
		fmt.Fprintf(os.Stderr, "sttmodels: %v\n", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	reports, err := loadReports(dir)
	if err != nil {
		return err
	}
	modelPaths, err := expandModels(*modelsFlag)
	if err != nil {
		return err
	}
	cache, err := loadTranscriptCache(expandHome(*cacheFlag))
	if err != nil {
		return err
	}
	labels, err := loadLabels(expandHome(*labelsFlag))
	if err != nil {
		return err
	}

	whisper.SetLogCallback(nil)
	if err := cache.transcribeMissing(reports, modelPaths); err != nil {
		return err
	}

	db.InitDB()
	stt.DisableStdoutTrace()
	models := util.MapSlice(modelPaths, modelName)
	decoded, err := decodeReports(reports, models, cache)
	if err != nil {
		return err
	}

	if *labelFlag {
		return labelReports(decoded, labels, bufio.NewReader(os.Stdin))
	}
	if *listFlag {
		printReports(decoded, labels)
	}
	printMetrics(decoded, models, labels)
	return nil
}

// report is a saved voice transmission.
type report struct {
	server.STTReport
	id      string // Path of the JSON file in the reports directory, without the extension
	wavPath string
}

// loadReports returns the transmissions in dir and its subdirectories that
// have audio, in the order of their paths.
func loadReports(dir string) ([]*report, error) {
	var reports []*report
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".json" {
			return err
		}
		wavPath := strings.TrimSuffix(path, ".json") + ".wav"
		if _, err := os.Stat(wavPath); errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}

		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		r := &report{wavPath: wavPath}
		if err := json.Unmarshal(b, &r.STTReport); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		r.id = filepath.ToSlash(strings.TrimSuffix(rel, ".json"))
		reports = append(reports, r)
		return nil
	})
	if err == nil && len(reports) == 0 {
		err = fmt.Errorf("%s: no transmissions with audio", dir)
	}
	return reports, err
}

// readSamples returns the samples of a 16 kHz mono 16-bit WAV file that
// vice wrote.
func readSamples(path string) ([]int16, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	const headerSize = 44
	if len(b) < headerSize {
		return nil, fmt.Errorf("%s: too short for a WAV file", path)
	}
	b = b[headerSize:]
	samples := make([]int16, len(b)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return samples, nil
}

// expandModels returns the model files that the -models flag names, in
// its order.
func expandModels(models string) ([]string, error) {
	var paths []string
	for pattern := range strings.SplitSeq(models, ",") {
		matches, err := filepath.Glob(expandHome(strings.TrimSpace(pattern)))
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("%s: no model files", pattern)
		}
		paths = append(paths, matches...)
	}
	return paths, nil
}

// modelName returns the name of the model in a file, as reports and the
// cache give it.
func modelName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".bin")
}

func expandHome(path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return path
}

// recognizer is a loaded speech recognition model.
type recognizer interface {
	transcribe(r *report, samples []int16) (string, error)
	close()
}

func loadRecognizer(path string) (recognizer, error) {
	if strings.Contains(filepath.Base(path), "parakeet") {
		m, err := whisper.LoadParakeetModel(path)
		if err != nil {
			return nil, err
		}
		return parakeetRecognizer{model: m}, nil
	}
	m, err := whisper.LoadModelFromFile(path)
	if err != nil {
		return nil, err
	}
	return &whisperRecognizer{model: m}, nil
}

type whisperRecognizer struct {
	model *whisper.Model
	mu    sync.Mutex
}

// transcribe runs whisper as vice ran it for the transmission: with the
// same prompt and the same decoding.
func (w *whisperRecognizer) transcribe(r *report, samples []int16) (string, error) {
	t := whisper.NewTranscriber(w.model, &w.mu, whisper.Options{
		Language:       "en",
		InitialPrompt:  r.WhisperPrompt,
		RealtimeFactor: r.WhisperRealtimeFactor,
	})
	t.AddSamples(samples)
	text, _, err := t.Stop()
	return text, err
}

func (w *whisperRecognizer) close() {
	w.model.Close()
}

type parakeetRecognizer struct {
	model *whisper.ParakeetModel
}

func (p parakeetRecognizer) transcribe(_ *report, samples []int16) (string, error) {
	audio := make([]float32, len(samples))
	for i, s := range samples {
		audio[i] = float32(s) / 32768
	}
	return p.model.Transcribe(audio)
}

func (p parakeetRecognizer) close() {
	p.model.Close()
}

// transcript is one model's transcript of a transmission. The cache file
// holds one per line.
type transcript struct {
	Report string `json:"report"`
	Model  string `json:"model"`
	Text   string `json:"text"`
	Ms     int64  `json:"ms"` // How long the model took on this computer
}

type transcriptKey struct {
	report, model string
}

type transcriptCache struct {
	path        string
	transcripts map[transcriptKey]transcript
}

func loadTranscriptCache(path string) (*transcriptCache, error) {
	c := &transcriptCache{path: path, transcripts: make(map[transcriptKey]transcript)}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	} else if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var t transcript
		if err := json.Unmarshal(sc.Bytes(), &t); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		c.transcripts[transcriptKey{t.Report, t.Model}] = t
	}
	return c, sc.Err()
}

func (c *transcriptCache) lookup(r *report, model string) (transcript, bool) {
	t, ok := c.transcripts[transcriptKey{r.id, model}]
	return t, ok
}

// transcribeMissing runs each model on the transmissions that it has not
// transcribed yet and adds the transcripts to the cache. It loads one model
// at a time.
func (c *transcriptCache) transcribeMissing(reports []*report, modelPaths []string) error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(c.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)

	for _, path := range modelPaths {
		name := modelName(path)
		missing := util.FilterSlice(reports, func(r *report) bool {
			_, ok := c.lookup(r, name)
			return !ok
		})
		if len(missing) == 0 {
			continue
		}

		fmt.Printf("%s: transcribing %d transmissions\n", name, len(missing))
		rec, err := loadRecognizer(path)
		if err != nil {
			return err
		}
		start := time.Now()
		for i, r := range missing {
			samples, err := readSamples(r.wavPath)
			if err != nil {
				rec.close()
				return err
			}
			tStart := time.Now()
			text, err := rec.transcribe(r, samples)
			if err != nil {
				// Not cached, so that the next run tries again.
				fmt.Fprintf(os.Stderr, "%s: %s: %v\n", r.id, name, err)
				continue
			}
			t := transcript{Report: r.id, Model: name, Text: text, Ms: time.Since(tStart).Milliseconds()}
			if err := enc.Encode(t); err != nil {
				rec.close()
				return err
			}
			c.transcripts[transcriptKey{r.id, name}] = t
			if (i+1)%100 == 0 {
				fmt.Printf("  %d of %d\n", i+1, len(missing))
			}
		}
		rec.close()
		fmt.Printf("  done in %s\n", time.Since(start).Round(time.Second))
	}
	return nil
}

// labels holds the correct command for each labeled transmission: the
// decoder output "CALLSIGN COMMANDS", or "" if the transmission gives no
// command.
type labels struct {
	path     string
	commands map[string]string // Report id to command
}

func loadLabels(path string) (*labels, error) {
	l := &labels{path: path, commands: make(map[string]string)}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	} else if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &l.commands); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return l, nil
}

func (l *labels) save() error {
	b, err := json.MarshalIndent(l.commands, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(l.path, b, 0o644)
}
