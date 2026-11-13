// speech/stt/tokens_test.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"strconv"
	"strings"
)

// Some tests give the words after the callsign as a list of Tokens, with
// numbers and altitudes already read. Token and ParseCommands let those
// tests state their input that way: the tokens are written out as the
// words they stand for and decoded as a transmission.

// TokenType says how a test token is to be read.
type TokenType int

const (
	TokenWord     TokenType = iota
	TokenNumber             // Text holds the digits
	TokenAltitude           // Value holds the altitude in hundreds of feet
)

// Token is one word of a test's input.
type Token struct {
	Text  string
	Type  TokenType
	Value int
}

// ParseCommands decodes the tokens as the instructions of a transmission
// to the aircraft.
func ParseCommands(tokens []Token, ac Aircraft) ([]string, float64) {
	r := readInstructions(tokenize(transcriptOf(tokens)), 0, &ac)
	return r.commands, 1
}

// transcriptOf writes test tokens out as the words they stand for.
func transcriptOf(tokens []Token) string {
	var words []string
	for _, t := range tokens {
		switch {
		case t.Type != TokenAltitude:
			words = append(words, t.Text)
		case strings.HasPrefix(t.Text, "FL"):
			words = append(words, "flight", "level", strconv.Itoa(t.Value))
		default:
			words = append(words, strconv.Itoa(t.Value*100))
		}
	}
	return strings.Join(words, " ")
}

// extractFix reads the tokens as one of the fixes and returns its
// identifier and the number of tokens it takes: the reading with the most
// evidence.
func extractFix(tokens []Token, fixes map[string]string) (string, float64, int) {
	d := newDecoding(thePhraseology(), tokenize(transcriptOf(tokens)), &Aircraft{Fixes: fixes})
	var best match
	for _, m := range d.readFix(0) {
		if m.evidence > best.evidence {
			best = m
		}
	}
	if best.end == 0 {
		return "", 0, 0
	}
	return best.values[0].(string), float64(best.evidence), best.end
}

// CallsignMatch is the reading of the start of a transmission as a
// callsign.
type CallsignMatch struct {
	Callsign   string
	Confidence float64
	Consumed   int
}

// MatchCallsign reads the start of the tokens as the callsign of one of
// the aircraft, and returns the tokens after it.
func MatchCallsign(tokens []Token, aircraft map[string]Aircraft) (CallsignMatch, []Token) {
	d := newDecoding(thePhraseology(), tokenize(transcriptOf(tokens)), nil)
	addressees := makeAddressees(aircraft)
	ms := d.matchCallsigns(addressees)
	if len(ms) == 0 {
		return CallsignMatch{}, tokens
	}
	m := ms[0]
	// Count the test tokens that the callsign's words came from.
	consumed, words := 0, 0
	for consumed < len(tokens) && words < m.end {
		words += len(tokenize(transcriptOf(tokens[consumed : consumed+1])))
		consumed++
	}
	return CallsignMatch{
		Callsign:   m.addressee.aircraft.Callsign,
		Confidence: float64(m.name+m.ident) / 2,
		Consumed:   consumed,
	}, tokens[consumed:]
}
