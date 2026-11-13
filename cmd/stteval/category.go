// cmd/stteval/category.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"strings"
)

// categoryRule maps a command pattern to its category. First match wins.
type categoryRule struct {
	match    func(cmd string) bool
	category string
}

// categoryRules defines command-to-category mappings, tried in order.
var categoryRules = []categoryRule{
	// Advisory commands: named explicitly since their letters collide
	// with the single-letter command prefixes below (CWT would read as a
	// C-approach, AP/1/5 as an A-crossing, TRAFFIC as a T-turn).
	{match: func(cmd string) bool { return cmd == "CWT" }, category: "wake"},
	{match: func(cmd string) bool { return cmd == "AP" || strings.HasPrefix(cmd, "AP/") }, category: "advisory"},
	{match: func(cmd string) bool { return cmd == "RP" || strings.HasPrefix(cmd, "RP/") }, category: "reporting_point"},
	// Joining an airway (JV1) or resuming a SID or STAR from a heading
	{match: func(cmd string) bool { return cmd[0] == 'J' || cmd == "RSID" || cmd == "RSTAR" }, category: "navigation"},
	{match: func(cmd string) bool { return strings.HasPrefix(cmd, "TRAFFIC") }, category: "traffic"},
	{match: func(cmd string) bool {
		return cmd == "VISSEP" || cmd == "RST" || cmd == "TO" || strings.HasPrefix(cmd, "TO/")
	}, category: ""},
	// T-prefix: turn commands (TL, TR) → heading
	{match: func(cmd string) bool {
		return len(cmd) > 1 && cmd[0] == 'T' && (cmd[1] == 'L' || cmd[1] == 'R')
	}, category: "heading"},
	// Bare turns to a heading (L270, R080) → heading; L/R followed by a
	// letter is left/right direct-to-fix (LDDARIC) → navigation.
	{match: func(cmd string) bool {
		return len(cmd) > 1 && (cmd[0] == 'L' || cmd[0] == 'R') && cmd[1] >= '0' && cmd[1] <= '9'
	}, category: "heading"},
	{match: func(cmd string) bool {
		return len(cmd) > 1 && (cmd[0] == 'L' || cmd[0] == 'R') && cmd[1] == 'D'
	}, category: "navigation"},
	// T-prefix: then-descend/climb (TD, TC) → altitude
	{match: func(cmd string) bool {
		return len(cmd) > 1 && cmd[0] == 'T' && (cmd[1] == 'D' || cmd[1] == 'C')
	}, category: "altitude"},
	// T-prefix: then-speed (TS) → speed
	{match: func(cmd string) bool {
		return len(cmd) > 1 && cmd[0] == 'T' && cmd[1] == 'S'
	}, category: "speed"},
	// Climb via SID / descend via STAR, with or without an except-maintain
	// altitude (CVS, DVS/A120) → altitude
	{match: isViaProcedure, category: "altitude"},
	// T-prefix: then-heading (TH) → heading
	{match: func(cmd string) bool {
		return len(cmd) > 1 && cmd[0] == 'T' && cmd[1] == 'H'
	}, category: "heading"},
	// D/C/A + digit → altitude (D40, C40, A40)
	{match: func(cmd string) bool {
		return len(cmd) > 1 && (cmd[0] == 'D' || cmd[0] == 'C' || cmd[0] == 'A') && cmd[1] >= '0' && cmd[1] <= '9'
	}, category: "altitude"},
	// D with /H → depart_heading
	{match: func(cmd string) bool {
		return cmd[0] == 'D' && strings.Contains(cmd, "/H")
	}, category: "depart_heading"},
	// D + letter → navigation (direct-to-fix)
	{match: func(cmd string) bool { return cmd[0] == 'D' }, category: "navigation"},
	// A with / → cleared_approach (at fix cleared approach: AHOLID/CI0L, AHOLID/I)
	{match: func(cmd string) bool {
		return cmd[0] == 'A' && strings.Contains(cmd, "/")
	}, category: "cleared_approach"},
	// A + letter → navigation
	{match: func(cmd string) bool { return cmd[0] == 'A' }, category: "navigation"},
	// C with / → crossing (cross fix at altitude/speed/mach: CFIX/A40, CFIX/S250, CFIX/M80)
	{match: func(cmd string) bool {
		return cmd[0] == 'C' && strings.Contains(cmd, "/")
	}, category: "crossing"},
	// C + letter without / → cleared_approach (CI9L)
	{match: func(cmd string) bool { return cmd[0] == 'C' }, category: "cleared_approach"},
	// I + fix → navigation (intercept a radial: IWAVEY/050). Bare I
	// (intercept the localizer) and ID stay uncategorized.
	{match: func(cmd string) bool { return cmd[0] == 'I' && strings.Contains(cmd, "/") }, category: "navigation"},
	// SAYAGAIN → no category
	{match: func(cmd string) bool { return strings.HasPrefix(cmd, "SAYAGAIN") }, category: ""},
	// S → speed
	{match: func(cmd string) bool { return cmd[0] == 'S' }, category: "speed"},
	// H → heading
	{match: func(cmd string) bool { return cmd[0] == 'H' }, category: "heading"},
	// E → expect_approach
	{match: func(cmd string) bool { return cmd[0] == 'E' }, category: "expect_approach"},
}

// isViaProcedure reports whether cmd is climb via SID or descend via STAR,
// with or without an except-maintain altitude.
func isViaProcedure(cmd string) bool {
	return isViaCommand(cmd, "CVS") || isViaCommand(cmd, "DVS")
}

// isViaCommand reports whether cmd is the given via command, plain or with
// an argument; a direct-to command for a fix that starts with "VS" is not.
func isViaCommand(cmd, via string) bool {
	return cmd == via || strings.HasPrefix(cmd, via+"/")
}

// commandCategory returns the category of an output command ("heading",
// "altitude", "speed", ...) based on its form, or "" if it has none.
func commandCategory(cmd string) string {
	if cmd == "" {
		return ""
	}
	for _, rule := range categoryRules {
		if rule.match(cmd) {
			return rule.category
		}
	}
	return ""
}
