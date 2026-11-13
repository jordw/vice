// cmd/sttsim/main.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

// cmd/sttsim provides STT debugging utilities.
//
// Usage:
//
//	# Compare how two words or phrases sound
//	go run ./cmd/sttsim "localizer" "laser"
//	go run ./cmd/sttsim "fly heading" "flighting"
//
//	# Show how a transcript is read
//	go run ./cmd/sttsim -norm "five hundred"
//	go run ./cmd/sttsim -norm "two thousand five hundred"
//
// Similarity mode shows the phonemes the decoder hears in each argument
// and the similarity it measures between them. The first argument is what
// the recognizer wrote and the second is the phraseology it is compared
// with.
//
// Normalization mode shows the words of a transcript as the decoder reads
// them, with numbers and altitudes combined.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/mmp/vice/speech/stt"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Check for -norm flag
	if os.Args[1] == "-norm" {
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "Usage: %s -norm <transcript>\n", os.Args[0])
			os.Exit(1)
		}
		transcript := strings.Join(os.Args[2:], " ")
		runNormalize(transcript)
		return
	}

	// Default: similarity comparison mode
	if len(os.Args) != 3 {
		printUsage()
		os.Exit(1)
	}

	w1, w2 := os.Args[1], os.Args[2]
	runSimilarity(w1, w2)
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  %s <word1> <word2>       Compare string similarity\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s -norm <transcript>   Test transcript normalization\n", os.Args[0])
}

func runNormalize(transcript string) {
	fmt.Printf("Input:  %q\n", transcript)
	var texts []string
	for _, w := range stt.ReadWords(transcript) {
		switch w.Kind {
		case stt.WordNumber:
			texts = append(texts, "number:"+w.Text)
		case stt.WordAltitude:
			texts = append(texts, "altitude:"+w.Text)
		default:
			texts = append(texts, w.Text)
		}
	}
	fmt.Printf("Output: %v\n", texts)
}

func runSimilarity(heard, target string) {
	fmt.Printf("Heard:  %q  [%s]\n", heard, stt.Pronunciation(heard))
	fmt.Printf("Target: %q  [%s]\n", target, stt.Pronunciation(target))
	fmt.Printf("Similarity: %.3f\n", stt.Similarity(strings.ToLower(heard), strings.ToLower(target)))
}
