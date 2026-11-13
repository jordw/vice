// speech/stt/phoneme.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"strings"

	"github.com/mmp/vice/math"
)

// This file defines the phoneme inventory and the articulatory distance
// between phonemes. The decoder never compares spellings: every word of
// the transcript and every word of the phraseology is converted to
// phonemes, and the cost of reading one as the other is the cost of
// aligning their phoneme strings. Speech recognition errors are acoustic,
// so sounds that are produced alike (m/n, t/d, f/th) substitute for one
// another cheaply, and unrelated sounds do not.

// phoneme is one speech sound, named after its ARPAbet symbol.
type phoneme uint8

const (
	phNone phoneme = iota
	// Vowels.
	phIY // bEEt
	phIH // bIt
	phEY // bAIt
	phEH // bEt
	phAE // bAt
	phAA // fAther
	phAO // bOUGHt
	phOW // bOAt
	phUH // bOOk
	phUW // bOOt
	phAH // bUt
	phAX // About (schwa)
	phAY // bIte
	phAW // bOUt
	phOY // bOY
	// Consonants.
	phP
	phB
	phT
	phD
	phK
	phG
	phF
	phV
	phTH // THin
	phDH // THen
	phS
	phZ
	phSH
	phZH
	phCH
	phJH
	phHH
	phM
	phN
	phNG
	phL
	phR
	phW
	phY
	numPhonemes
)

var phonemeNames = [numPhonemes]string{
	phNone: "-",
	phIY:   "IY", phIH: "IH", phEY: "EY", phEH: "EH", phAE: "AE", phAA: "AA", phAO: "AO", phOW: "OW",
	phUH: "UH", phUW: "UW", phAH: "AH", phAX: "AX", phAY: "AY", phAW: "AW", phOY: "OY",
	phP: "P", phB: "B", phT: "T", phD: "D", phK: "K", phG: "G", phF: "F", phV: "V", phTH: "TH", phDH: "DH",
	phS: "S", phZ: "Z", phSH: "SH", phZH: "ZH", phCH: "CH", phJH: "JH", phHH: "HH", phM: "M", phN: "N",
	phNG: "NG", phL: "L", phR: "R", phW: "W", phY: "Y",
}

var phonemesByName = func() map[string]phoneme {
	m := make(map[string]phoneme)
	for p, name := range phonemeNames {
		m[name] = phoneme(p)
	}
	return m
}()

func (p phoneme) String() string { return phonemeNames[p] }

func (p phoneme) isVowel() bool { return p >= phIY && p <= phOY }

// pronunciation is the phoneme string of a word or phrase.
type pronunciation []phoneme

func (pr pronunciation) String() string {
	names := make([]string, len(pr))
	for i, p := range pr {
		names[i] = p.String()
	}
	return strings.Join(names, " ")
}

// parsePronunciation converts space-separated ARPAbet symbols to phonemes.
// It panics on an unknown symbol: pronunciations are written in the source.
func parsePronunciation(s string) pronunciation {
	var pr pronunciation
	for name := range strings.FieldsSeq(s) {
		p, ok := phonemesByName[name]
		if !ok {
			panic("stt: unknown phoneme " + name)
		}
		pr = append(pr, p)
	}
	return pr
}

// Articulatory features. Vowels are placed by tongue height and backness,
// consonants by place and manner of articulation and by voicing.
type manner uint8

const (
	mannerVowel manner = iota
	mannerStop
	mannerAffricate
	mannerFricative
	mannerNasal
	mannerLateral
	mannerRhotic
	mannerGlide
)

type features struct {
	manner manner
	// Consonants: place runs from the lips (0) back to the glottis (7).
	// Vowels: height runs from close (0) to open (3), backness from front
	// (0) to back (2).
	place, height, backness int8
	voiced                  bool
	glide                   bool // vowel that moves (diphthong)
}

var phonemeFeatures = [numPhonemes]features{
	phIY: {height: 0, backness: 0},
	phIH: {height: 1, backness: 0},
	phEY: {height: 1, backness: 0, glide: true},
	phEH: {height: 2, backness: 0},
	phAE: {height: 3, backness: 0},
	phAA: {height: 3, backness: 2},
	phAO: {height: 2, backness: 2},
	phOW: {height: 1, backness: 2, glide: true},
	phUH: {height: 1, backness: 2},
	phUW: {height: 0, backness: 2},
	phAH: {height: 2, backness: 1},
	phAX: {height: 2, backness: 1},
	phAY: {height: 3, backness: 1, glide: true},
	phAW: {height: 3, backness: 1, glide: true},
	phOY: {height: 2, backness: 2, glide: true},

	phP:  {manner: mannerStop, place: 0},
	phB:  {manner: mannerStop, place: 0, voiced: true},
	phT:  {manner: mannerStop, place: 3},
	phD:  {manner: mannerStop, place: 3, voiced: true},
	phK:  {manner: mannerStop, place: 6},
	phG:  {manner: mannerStop, place: 6, voiced: true},
	phF:  {manner: mannerFricative, place: 1},
	phV:  {manner: mannerFricative, place: 1, voiced: true},
	phTH: {manner: mannerFricative, place: 2},
	phDH: {manner: mannerFricative, place: 2, voiced: true},
	phS:  {manner: mannerFricative, place: 3},
	phZ:  {manner: mannerFricative, place: 3, voiced: true},
	phSH: {manner: mannerFricative, place: 4},
	phZH: {manner: mannerFricative, place: 4, voiced: true},
	phCH: {manner: mannerAffricate, place: 4},
	phJH: {manner: mannerAffricate, place: 4, voiced: true},
	phHH: {manner: mannerFricative, place: 7},
	phM:  {manner: mannerNasal, place: 0, voiced: true},
	phN:  {manner: mannerNasal, place: 3, voiced: true},
	phNG: {manner: mannerNasal, place: 6, voiced: true},
	phL:  {manner: mannerLateral, place: 3, voiced: true},
	phR:  {manner: mannerRhotic, place: 4, voiced: true},
	phW:  {manner: mannerGlide, place: 0, voiced: true},
	phY:  {manner: mannerGlide, place: 5, voiced: true},
}

// The costs below are in units where 1 is the cost of replacing a sound
// with an unrelated one.
const (
	costVoicing       = 0.25 // t/d, s/z
	costPlaceStep     = 0.12 // per step along the vocal tract
	costPlaceMax      = 0.45
	costVowelHeight   = 0.08 // per step of tongue height
	costVowelBackness = 0.06 // per step of tongue backness
	costVowelGlide    = 0.06 // diphthong against a pure vowel
	costVowelMax      = 0.30
	costConsonantMax  = 0.80 // one consonant for an unrelated one
	costUnrelated     = 1.0  // a vowel for a consonant
)

// mannerCost is the cost of a change in manner of articulation between two
// consonants; pairs that are not listed are unrelated.
var mannerCost = map[[2]manner]float32{
	{mannerStop, mannerAffricate}:      0.35,
	{mannerAffricate, mannerFricative}: 0.35,
	{mannerStop, mannerFricative}:      0.45,
	{mannerStop, mannerNasal}:          0.55,
	{mannerLateral, mannerRhotic}:      0.40,
	{mannerLateral, mannerNasal}:       0.55,
	{mannerRhotic, mannerGlide}:        0.45,
	{mannerLateral, mannerGlide}:       0.55,
	{mannerFricative, mannerGlide}:     0.70,
}

// substitutionCosts[a][b] is the cost of hearing phoneme a as phoneme b.
var substitutionCosts = func() (c [numPhonemes][numPhonemes]float32) {
	for a := phoneme(1); a < numPhonemes; a++ {
		for b := phoneme(1); b < numPhonemes; b++ {
			c[a][b] = substitutionCost(a, b)
		}
	}
	return
}()

func substitutionCost(a, b phoneme) float32 {
	if a == b {
		return 0
	}
	fa, fb := phonemeFeatures[a], phonemeFeatures[b]
	if a.isVowel() != b.isVowel() {
		// A glide is close to the vowel it resembles.
		v, c := a, b
		if b.isVowel() {
			v, c = b, a
		}
		switch {
		case c == phW && (v == phUW || v == phUH || v == phOW):
			return 0.5
		case c == phY && (v == phIY || v == phIH):
			return 0.5
		}
		return costUnrelated
	}

	if a.isVowel() {
		// Unstressed vowels reduce toward schwa, and a spelling says little
		// about which vowel was heard: every vowel is close to schwa.
		if a == phAX || b == phAX {
			return 0.10
		}
		cost := costVowelHeight*float32(math.Abs(fa.height-fb.height)) +
			costVowelBackness*float32(math.Abs(fa.backness-fb.backness))
		if fa.glide != fb.glide {
			cost += costVowelGlide
		}
		return min(cost, costVowelMax)
	}

	var cost float32
	if fa.manner != fb.manner {
		mc, ok := mannerCost[[2]manner{fa.manner, fb.manner}]
		if !ok {
			mc, ok = mannerCost[[2]manner{fb.manner, fa.manner}]
		}
		if !ok {
			return costConsonantMax
		}
		cost += mc
	}
	if fa.voiced != fb.voiced {
		cost += costVoicing
	}
	cost += min(costPlaceStep*float32(math.Abs(fa.place-fb.place)), costPlaceMax)
	// The dental fricatives are heard as the stops beside them ("tree" for
	// "three", "dat" for "that").
	if (a == phTH && b == phT) || (a == phT && b == phTH) || (a == phDH && b == phD) || (a == phD && b == phDH) {
		cost = 0.2
	}
	return min(cost, costConsonantMax)
}

// deletionCosts[p] is the cost of a phoneme that one side has and the
// other lacks. Weak sounds drop out of fast speech cheaply.
var deletionCosts = func() (c [numPhonemes]float32) {
	for p := phoneme(1); p < numPhonemes; p++ {
		switch {
		case p == phAX:
			c[p] = 0.3
		case p.isVowel():
			c[p] = 0.55
		case p == phHH:
			c[p] = 0.35
		case p == phW || p == phY:
			c[p] = 0.5
		case p == phT || p == phD:
			c[p] = 0.6
		default:
			c[p] = 0.75
		}
	}
	return
}()

// finalDiscount scales the cost of a sound that one pronunciation has at
// its end and the other lacks. The end of a word is where the recognizer
// most often adds or drops a sound ("safe" for "say", "localiz" for
// "localizer").
const finalDiscount = 0.5

// alignmentCost returns the least cost of editing pronunciation a into
// pronunciation b, where substituting a phoneme costs their articulatory
// distance and inserting or deleting one costs its deletion cost.
func alignmentCost(a, b pronunciation) float32 {
	prev := make([]float32, len(b)+1)
	cur := make([]float32, len(b)+1)
	for j, p := range b {
		prev[j+1] = prev[j] + deletionCosts[p]
	}
	for i, pa := range a {
		cur[0] = prev[0] + deletionCosts[pa]
		for j, pb := range b {
			del, ins := deletionCosts[pa], deletionCosts[pb]
			// A trailing sound: everything of the other side is used up.
			if i == len(a)-1 && j == len(b)-1 {
				del, ins = del*finalDiscount, ins*finalDiscount
			}
			cur[j+1] = min(prev[j]+substitutionCosts[pa][pb], prev[j+1]+del, cur[j]+ins)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// weight returns the cost of deleting every phoneme of a pronunciation: a
// measure of how much sound it has.
func (pr pronunciation) weight() float32 {
	var w float32
	for _, p := range pr {
		w += deletionCosts[p]
	}
	return w
}

// soundSimilarity returns how alike two pronunciations sound, from 0
// (unrelated) to 1 (identical): one minus the alignment cost as a
// fraction of the weight of the longer pronunciation.
func soundSimilarity(a, b pronunciation) float32 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	return max(0, 1-alignmentCost(a, b)/max(a.weight(), b.weight()))
}
