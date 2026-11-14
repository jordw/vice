// cmd/sttmodels/results.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/mmp/vice/speech/stt"
	"github.com/mmp/vice/util"
)

// clientModel is the name of the outcome of the transcript in a report,
// which the user's own whisper model made.
const clientModel = "client"

// outcome is what one model made of a transmission.
type outcome struct {
	model      string
	transcript string
	command    string // The decoder's output, "CALLSIGN COMMANDS", or "" if it found no command
	ms         int64  // How long the model took on this computer, or 0 for the client
}

// decodedReport is a transmission and what each model made of it.
type decodedReport struct {
	*report
	outcomes     []outcome // The client's first, then those of the models that transcribed it, in -models order
	consensus    string    // The command that more than half of at least three models give, or its equivalent
	hasConsensus bool
}

// decodeReports decodes the transcript in each report and the cached
// transcripts of the models with the current decoder.
func decodeReports(reports []*report, models []string, cache *transcriptCache) ([]*decodedReport, error) {
	decoder := stt.NewTranscriber(nil)
	var decoded []*decodedReport
	for _, r := range reports {
		d := &decodedReport{report: r}
		add := func(model, text string, ms int64) error {
			o := outcome{model: model, transcript: text, ms: ms}
			// vice does not decode a transcript in which whisper heard nothing.
			if text != "" && text != "[BLANK_AUDIO]" {
				var err error
				if o.command, err = decoder.DecodeTranscript(r.STTAircraft, text, r.ControllerRadioName); err != nil {
					return fmt.Errorf("%s: %w", r.id, err)
				}
			}
			d.outcomes = append(d.outcomes, o)
			return nil
		}

		if err := add(clientModel, r.Transcript, 0); err != nil {
			return nil, err
		}
		for _, m := range models {
			if t, ok := cache.lookup(r, m); ok {
				if err := add(m, t.Text, t.Ms); err != nil {
					return nil, err
				}
			}
		}
		d.consensus, d.hasConsensus = findConsensus(d.outcomes[1:], r.STTAircraft)
		decoded = append(decoded, d)
	}
	return decoded, nil
}

func findConsensus(outcomes []outcome, aircraft map[string]stt.Aircraft) (string, bool) {
	if len(outcomes) < 3 {
		return "", false
	}
	for _, c := range outcomes {
		n := len(util.FilterSlice(outcomes, func(o outcome) bool {
			return stt.CommandsEquivalent(c.command, o.command, aircraft)
		}))
		if 2*n > len(outcomes) {
			return c.command, true
		}
	}
	return "", false
}

// unanimous reports whether every model gives the same command.
func (d *decodedReport) unanimous() bool {
	return !slices.ContainsFunc(d.outcomes, func(o outcome) bool {
		return !stt.CommandsEquivalent(d.outcomes[0].command, o.command, d.STTAircraft)
	})
}

// callsigns returns the callsigns in the transmission's aircraft context.
func (d *decodedReport) callsigns() []string {
	cs := make(map[string]bool)
	for _, ac := range d.STTAircraft {
		cs[ac.Callsign] = true
	}
	return util.SortedMapKeys(cs)
}

// matches reports whether an outcome's command is the labeled one, by the
// rules of the tests in speech/stt/tests.
func matches(o outcome, label string, aircraft map[string]stt.Aircraft) bool {
	callsign, command, _ := strings.Cut(label, " ")
	tf := stt.TestFile{Transcript: o.transcript, Callsign: callsign, Command: command}
	return tf.Matches(o.command, aircraft)
}

func sameCallsign(a, b string) bool {
	a, _, _ = strings.Cut(a, " ")
	b, _, _ = strings.Cut(b, " ")
	return strings.TrimSuffix(a, "/T") == strings.TrimSuffix(b, "/T")
}

func showCommand(command string) string {
	if command == "" {
		return "(none)"
	}
	return command
}

// modelStats counts how one model did.
type modelStats struct {
	n, commands                 int
	withConsensus, agree        int
	labeled, correct            int
	labeledCallsign, callsignOK int
	ms                          []int64
}

func printMetrics(decoded []*decodedReport, models []string, labels *labels) {
	stats := make(map[string]*modelStats)
	for _, m := range append([]string{clientModel}, models...) {
		stats[m] = &modelStats{}
	}

	var nLabeled, nConsensus, nReplayed, nSameReplay int
	for _, d := range decoded {
		label, labeled := labels.commands[d.id]
		if labeled {
			nLabeled++
		}
		if d.hasConsensus {
			nConsensus++
		}

		for _, o := range d.outcomes {
			s := stats[o.model]
			s.n++
			if o.command != "" {
				s.commands++
			}
			if d.hasConsensus {
				s.withConsensus++
				if stt.CommandsEquivalent(d.consensus, o.command, d.STTAircraft) {
					s.agree++
				}
			}
			if labeled {
				s.labeled++
				if matches(o, label, d.STTAircraft) {
					s.correct++
				}
				if label != "" {
					s.labeledCallsign++
					if sameCallsign(label, o.command) {
						s.callsignOK++
					}
				}
			}
			if o.model != clientModel {
				s.ms = append(s.ms, o.ms)
			}

			if o.model == modelName(d.WhisperModel) {
				nReplayed++
				if o.transcript == d.Transcript {
					nSameReplay++
				}
			}
		}
	}

	fmt.Printf("%d transmissions: %d labeled, %d with a consensus\n", len(decoded), nLabeled, nConsensus)
	if nReplayed > 0 {
		fmt.Printf("The user's own whisper model gave the user's transcript again for %d of %d\n", nSameReplay, nReplayed)
	}
	fmt.Println()

	fmt.Printf("%-40s %6s %8s %10s %8s %9s %7s %7s\n", "model", "n", "command", "consensus", "correct", "callsign",
		"p50 ms", "p90 ms")
	for _, m := range append([]string{clientModel}, models...) {
		s := stats[m]
		slices.Sort(s.ms)
		fmt.Printf("%-40s %6d %8s %10s %8s %9s %7s %7s\n", m, s.n, percent(s.commands, s.n),
			percent(s.agree, s.withConsensus), percent(s.correct, s.labeled), percent(s.callsignOK, s.labeledCallsign),
			percentile(s.ms, 0.5), percentile(s.ms, 0.9))
	}

	fmt.Print(`
client:    the transcript that the user's own whisper model gave
command:   transmissions in which the decoder found a command
consensus: transmissions with a consensus in which the model gave it
correct:   labeled transmissions in which the model gave the labeled command
callsign:  labeled transmissions with a command in which the model gave its callsign
p50, p90:  transcription times on this computer
`)
}

func percent(n, total int) string {
	if total == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(total))
}

// percentile returns the value at fraction p of the sorted values.
func percentile(sorted []int64, p float64) string {
	if len(sorted) == 0 {
		return "-"
	}
	return strconv.FormatInt(sorted[int(p*float64(len(sorted)-1))], 10)
}

func printReports(decoded []*decodedReport, labels *labels) {
	for _, d := range decoded {
		fmt.Print(d.id)
		if label, ok := labels.commands[d.id]; ok {
			fmt.Printf("  label: %s", showCommand(label))
		}
		fmt.Println()
		for _, o := range d.outcomes {
			name := o.model
			if o.model == clientModel {
				name += " " + modelName(d.WhisperModel)
			}
			fmt.Printf("  %-48s %q -> %s\n", name, o.transcript, showCommand(o.command))
		}
	}
	fmt.Println()
}

// labelReports asks for the correct commands of the unlabeled
// transmissions and saves each one in labels.
func labelReports(decoded []*decodedReport, labels *labels, in *bufio.Reader) error {
	todo := util.FilterSlice(decoded, func(d *decodedReport) bool {
		_, ok := labels.commands[d.id]
		return !ok
	})
	// The transmissions on which the models disagree tell the most about
	// them, so they come first.
	todo = append(util.FilterSlice(todo, func(d *decodedReport) bool { return !d.unanimous() }),
		util.FilterSlice(todo, (*decodedReport).unanimous)...)

	for i, d := range todo {
		fmt.Printf("\n[%d of %d] %s (%.1f s)\n", i+1, len(todo), d.wavPath, d.AudioDurationMs/1000)
		fmt.Printf("  aircraft: %s\n", strings.Join(d.callsigns(), " "))
		for _, o := range d.outcomes {
			fmt.Printf("  %-40s %q\n", o.model, o.transcript)
		}

		var choices []string
		for _, o := range d.outcomes {
			if !slices.Contains(choices, o.command) {
				choices = append(choices, o.command)
			}
		}
		for j, c := range choices {
			var models []string
			for _, o := range d.outcomes {
				if o.command == c {
					models = append(models, o.model)
				}
			}
			fmt.Printf("  %d) %-30s %s\n", j+1, showCommand(c), strings.Join(models, ", "))
		}

		command, a := askLabel(d, choices, in)
		if a == answerQuit {
			return nil
		} else if a == answerSkip {
			continue
		}
		labels.commands[d.id] = command
		if err := labels.save(); err != nil {
			return err
		}
	}
	return nil
}

type answer int

const (
	answerCommand answer = iota
	answerSkip
	answerQuit
)

// askLabel plays a transmission and reads its correct command.
func askLabel(d *decodedReport, choices []string, in *bufio.Reader) (string, answer) {
	player := playAudio(d.wavPath)
	defer func() { stopAudio(player) }()

	callsigns := d.callsigns()
	for {
		fmt.Print("Command (number, CALLSIGN COMMANDS, or none; Enter replays, s skips, q quits): ")
		line, err := in.ReadString('\n')
		if err != nil {
			return "", answerQuit
		}
		line = strings.Join(strings.Fields(strings.ToUpper(line)), " ")

		switch line {
		case "":
			stopAudio(player)
			player = playAudio(d.wavPath)
		case "Q":
			return "", answerQuit
		case "S":
			return "", answerSkip
		case "NONE":
			return "", answerCommand
		default:
			if n, err := strconv.Atoi(line); err == nil {
				if n >= 1 && n <= len(choices) {
					return choices[n-1], answerCommand
				}
				fmt.Printf("There is no command %d.\n", n)
			} else if callsign, _, _ := strings.Cut(line, " "); !slices.Contains(callsigns, callsign) {
				fmt.Printf("%s is not an aircraft in the transmission's context.\n", callsign)
			} else {
				return line, answerCommand
			}
		}
	}
}

// playAudio starts to play a WAV file, if sttmodels knows a player for
// this system.
func playAudio(path string) *exec.Cmd {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("afplay", path)
	case "linux":
		cmd = exec.Command("aplay", "-q", path)
	default:
		return nil
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", cmd.Path, err)
		return nil
	}
	return cmd
}

func stopAudio(cmd *exec.Cmd) {
	if cmd != nil {
		cmd.Process.Kill()
		cmd.Wait()
	}
}
