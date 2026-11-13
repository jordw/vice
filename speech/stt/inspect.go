// speech/stt/inspect.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"fmt"
	"strconv"
	"sync"
)

// This file gives the tools that examine transcripts (cmd/sttsim,
// cmd/stteval) a view of how the decoder reads them.

// WordKind says how the decoder reads a word of a transcript.
type WordKind int

const (
	WordPlain    WordKind = iota
	WordNumber            // Text holds the digits that were said
	WordAltitude          // Value holds the altitude in hundreds of feet
)

// Word is a word of a transcript, or several that make one number, as
// the decoder reads them.
type Word struct {
	Text  string
	Kind  WordKind
	Value int
}

// ReadWords returns the words of a transcript, with each run of number
// words read as one number and each altitude said in full as one
// altitude.
func ReadWords(transcript string) []Word {
	d := newDecoding(thePhraseology(), tokenize(transcript), nil)
	var words []Word
	for pos := 0; pos < len(d.tokens); {
		// The longest reading at this position wins.
		var altitude match
		for _, m := range d.readThousands(pos, true) {
			if m.end > altitude.end {
				altitude = m
			}
		}
		// A number starts at a number word, not at a word that could be
		// misheard as one.
		var number numberReading
		if isNumberWord(d.tokens[pos].text) {
			for _, r := range d.readNumbers(pos) {
				if r.end > number.end {
					number = r
				}
			}
		}
		switch {
		case altitude.end > pos && altitude.end >= number.end:
			alt := altitude.values[0].(int)
			words = append(words, Word{Text: strconv.Itoa(alt), Kind: WordAltitude, Value: alt})
			pos = altitude.end
		case number.end > pos:
			words = append(words, Word{Text: number.digits, Kind: WordNumber, Value: number.value()})
			pos = number.end
		default:
			words = append(words, Word{Text: d.tokens[pos].text})
			pos++
		}
	}
	return words
}

// IsStopword reports whether the decoder passes over a word at no cost:
// the small words of the language and hesitations.
func IsStopword(word string) bool { return isStopword(word) }

// Pronunciation returns the phonemes the decoder hears in the words, as
// ARPAbet symbols.
func Pronunciation(words string) string { return pronouncePhrase(words).String() }

// Similarity returns how closely the heard words sound like the target
// words, from 0 to 1, as the decoder measures it.
func Similarity(heard, target string) float32 {
	return similarity(heard, makeVocabWord(target))
}

// LogBuffer captures STT processing logs for bug reports.
type LogBuffer struct {
	lines []string
	mu    sync.Mutex
}

// logToStdout says whether the trace of every decode is also printed. The
// tests turn it off.
var logToStdout = true

var (
	currentLogBuffer *LogBuffer
	logBufferMu      sync.Mutex
)

// StartCapture begins capturing log lines to a buffer.
// Returns the buffer that will collect the logs.
func StartCapture() *LogBuffer {
	logBufferMu.Lock()
	defer logBufferMu.Unlock()
	buf := &LogBuffer{lines: make([]string, 0, 50)}
	currentLogBuffer = buf
	return buf
}

// StopCapture stops capturing and returns the captured lines.
func StopCapture() []string {
	logBufferMu.Lock()
	defer logBufferMu.Unlock()
	if currentLogBuffer == nil {
		return nil
	}
	buf := currentLogBuffer
	currentLogBuffer = nil
	buf.mu.Lock()
	defer buf.mu.Unlock()
	return buf.lines
}

// logLocalStt logs local STT processing details.
// Also captures to buffer if capture is active.
func logLocalStt(format string, args ...any) {
	line := fmt.Sprintf("[local-stt] "+format, args...)
	if logToStdout {
		fmt.Println(line)
	}

	logBufferMu.Lock()
	buf := currentLogBuffer
	logBufferMu.Unlock()

	if buf != nil {
		buf.mu.Lock()
		buf.lines = append(buf.lines, line)
		buf.mu.Unlock()
	}
}
