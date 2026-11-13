// speech/stt/g2p.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"slices"
	"strings"
	"sync"
)

// This file converts spellings to phonemes. The transcript is the speech
// recognizer's spelling of what it heard, so it contains ordinary English
// words, misheard words, and invented spellings of names; the phraseology
// and the aircraft context supply the words the controller could have
// said. Both sides go through the same conversion: a lexicon of words
// whose pronunciation the rules get wrong, then letter-to-sound rules for
// everything else.

// letterToSoundRules are context-sensitive rewrite rules in the notation
// of the Naval Research Laboratory letter-to-sound system (Elovitz et al.,
// "Automatic Translation of English Text to Phonetics by Means of
// Letter-to-Sound Rules", 1976). A rule reads left[match]right=phonemes:
// the letters in brackets become the phonemes when the text before and
// after them fits the left and right context patterns. The rules for a
// letter are tried in order and the first that fits applies. In the
// context patterns:
//
//	#  one or more vowels
//	:  zero or more consonants
//	^  one consonant
//	.  one voiced consonant (B D V G J L M N R W Z)
//	+  one front vowel (E I Y)
//	%  a suffix: ER, E, ES, ED, ING, ELY (right context only)
//	&  a sibilant: S C G Z X J CH SH (left context only)
//	@  one of T S R D L Z N J TH CH SH (left context only)
//	_  a word boundary
//
// Any other character matches itself.
const letterToSoundRules = `
_[A]_=AX
_[ARE]_=AA R
_[AR]O=AX R
[AR]#=EH R
^[AS]#=EY S
[A]WA=AX
[AW]=AO
_:[ANY]=EH N IY
[A]^+#=EY
#:[ALLY]=AX L IY
_[AL]#=AX L
[AGAIN]=AX G EH N
#:[AG]E=IH JH
[A]^+:#=AE
_:[A]^+_=EY
[A]^%=EY
_[ARR]=AX R
[ARR]=AE R
_:[AR]_=AA R
[AR]_=AX R
[AR]=AA R
[AIR]=EH R
[AI]=EY
[AY]=EY
[AU]=AO
#:[AL]_=AX L
#:[ALS]_=AX L Z
[ALK]=AO K
[AL]^=AO L
_:[ABLE]=EY B AX L
[ABLE]=AX B AX L
[ANG]+=EY N JH
[A]=AE

_[BE]^#=B IH
[BEING]=B IY IH NG
_[BOTH]_=B OW TH
_[BUS]#=B IH Z
[BUIL]=B IH L
[BB]=B
[B]=B

_[CH]^=K
^E[CH]=K
[CH]=CH
_S[CI]#=S AY
[CI]A=SH
[CI]O=SH
[CI]EN=SH
[C]+=S
[CK]=K
[COM]%=K AH M
[CC]+=K S
[CC]=K
[C]=K

#:[DED]_=D IH D
.E[D]_=D
#^:E[D]_=T
_[DE]^#=D IH
_[DO]_=D UW
_[DOES]=D AH Z
_[DOING]=D UW IH NG
_[DOW]=D AW
[DU]A=JH UW
[DD]=D
[DG]=JH
[D]=D

#:[E]_=
'^:[E]_=
_:[E]_=IY
#[ED]_=D
#:[E]D_=
[EV]AX R=EH V
[E]^%=IY
[ERI]#=IY R IY
[ERI]=EH R IH
#:[AX R]#=AX R
[AX R]#=EH R
[AX R]=AX R
_[EVEN]=IY V EH N
#:[E]W=
@[EW]=UW
[EW]=Y UW
[E]O=IY
#:&[ES]_=IH Z
#:[E]S_=
#:[ELY]_=L IY
#:[EMENT]=M EH N T
[EFUL]=F UH L
[EE]=IY
[EARN]=AX R N
_[EAR]^=AX R
[EAD]=EH D
#:[EA]_=IY AX
[EA]SU=EH
[EA]=IY
[EIGH]=EY
[EI]=IY
_[EYE]=AY
[EY]=IY
[EU]=Y UW
[E]=EH

[FUL]=F UH L
[FF]=F
[F]=F

[GIV]=G IH V
_[G]I^=G
[GE]T=G EH
SU[GGES]=G JH EH S
[GG]=G
_B#[G]=G
[G]+=JH
[GREAT]=G R EY T
#[GH]=
[GH]=G
[G]=G

_[HAV]=HH AE V
_[HERE]=HH IY R
_[HOUR]=AW AX R
[HOW]=HH AW
[H]#=HH
[H]=

_[IN]=IH N
_[I]_=AY
[IN]D=AY N
[IER]=IY AX R
#:R[IED]_=IY D
[IED]_=AY D
[IEN]=IY EH N
[IE]T=AY EH
_:[I]%=AY
[I]%=IY
[IE]=IY
[I]^+:#=IH
[IR]#=AY R
[IZ]%=AY Z
[IS]%=AY Z
[I]D%=AY
+^[I]^+=IH
[I]T%=AY
#^:[I]^+=IH
[I]^+=AY
[IR]=AX R
[IGH]=AY
[ILD]=AY L D
[IGN]_=AY N
[IGN]^=AY N
[IGN]%=AY N
[IQUE]=IY K
_^[I]_=AY
^[I]_=IY
[I]=IH

[J]=JH

_[K]N=
[KK]=K
[K]=K

[LO]C#=L OW
L[L]=
#^:[L]%=AX L
[LEAD]=L IY D
[L]=L

[MOV]=M UW V
[MM]=M
[M]=M

E[NG]+=N JH
[NG]R=NG G
[NG]#=NG G
[NGL]%=NG G AX L
[NG]=NG
[NK]=NG K
_[NOW]_=N AW
[NN]=N
[N]=N

[OF]_=AX V
[OROUGH]=AX R OW
#:[OR]_=AX R
#:[ORS]_=AX R Z
[OR]=AO R
_[ONE]=W AH N
[OW]=OW
_[OVER]=OW V AX R
[OV]=AH V
[O]^%=OW
[O]^EN=OW
[O]^I#=OW
[OL]D=OW L
[OUGHT]=AO T
[OUGH]=AH F
_[OU]=AW
H[OU]S#=AW
[OUS]=AX S
[OUR]=AO R
[OULD]=UH D
^[OU]^L=AH
[OUP]=UW P
[OU]=AW
[OY]=OY
[OING]=OW IH NG
[OI]=OY
[OOR]=AO R
[OOK]=UH K
[OOD]=UH D
[OO]=UW
[O]E=OW
[O]_=OW
[OA]=OW
_[ONLY]=OW N L IY
_[ONCE]=W AH N S
[ON'T]=OW N T
C[O]N=AA
[O]NG=AO
_^:[O]N=AH
I[ON]=AX N
#:[ON]_=AX N
#^[ON]=AX N
[O]ST_=OW
[OF]^=AO F
[OTHER]=AH DH AX R
[OSS]_=AO S
#^:[OM]=AH M
[O]=AA

[PH]=F
[PEOP]=P IY P
[POW]=P AW
[PUT]_=P UH T
[PP]=P
[P]=P

[QUAR]=K W AO R
[QU]=K W
[Q]=K

_[RE]^#=R IY
[RR]=R
[R]=R

[SH]=SH
#[SION]=ZH AX N
[SOME]=S AH M
#[SUR]#=ZH AX R
[SUR]#=SH AX R
#[SU]#=ZH UW
#[SSU]#=SH UW
#[SED]_=Z D
#[S]#=Z
[SAID]=S EH D
^[SION]=SH AX N
[S]S=
.[S]_=Z
#:.E[S]_=Z
#^:##[S]_=Z
#^:#[S]_=S
U[S]_=S
_:#[S]_=Z
_[SCH]=S K
[S]C+=
#[SM]=Z M
#[SN]'=Z AX N
[S]=S

_[THE]_=DH AX
[TO]_=T UW
[THAT]_=DH AE T
_[THIS]_=DH IH S
_[THEY]=DH EY
_[THERE]=DH EH R
[THER]=DH AX R
[THEIR]=DH EH R
_[THAN]_=DH AE N
_[THEM]_=DH EH M
[THESE]_=DH IY Z
_[THEN]=DH EH N
[THROUGH]=TH R UW
[THOSE]=DH OW Z
[THOUGH]_=DH OW
_[THUS]=DH AH S
[TH]=TH
#:[TED]_=T IH D
S[TI]#N=CH
[TI]O=SH
[TI]A=SH
[TIEN]=SH AX N
[TUR]#=CH AX R
[TU]A=CH UW
_[TWO]=T UW
[TCH]=CH
[TT]=T
[T]=T

_[UN]I=Y UW N
_[UN]=AH N
_[UPON]=AX P AO N
@[UR]#=UH R
[UR]#=Y UH R
[UR]=AX R
[U]^_=AH
[U]^^=AH
[UY]=AY
_G[U]#=
G[U]%=
G[U]#=W
#N[U]=Y UW
@[U]=UW
[U]=Y UW

[VIEW]=V Y UW
[V]=V

_[WERE]=W AX R
[WA]S=W AA
[WA]T=W AA
[WHERE]=W EH R
[WHAT]=W AA T
[WHOL]=HH OW L
[WHO]=HH UW
[WH]=W
[WAR]=W AO R
[WOR]^=W AX R
[WR]=R
[W]=W

[X]=K S

[YOUNG]=Y AH NG
_[YOU]=Y UW
_[YES]=Y EH S
_[Y]=Y
#^:[Y]_=IY
#^:[Y]I=IY
_:[Y]_=AY
_:[Y]#=AY
_:[Y]^+:#=IH
_:[Y]^#=AY
[Y]=IH

[ZZ]=Z
[Z]=Z
`

// soundRule is one compiled letter-to-sound rule.
type soundRule struct {
	left, match, right string
	sounds             pronunciation
}

// soundRules holds the compiled rules, indexed by the first letter of the
// text they match.
var soundRules = sync.OnceValue(func() (rules [26][]soundRule) {
	for line := range strings.SplitSeq(letterToSoundRules, "\n") {
		if line == "" {
			continue
		}
		pattern, sounds, _ := strings.Cut(line, "=")
		left, rest, _ := strings.Cut(pattern, "[")
		match, right, _ := strings.Cut(rest, "]")
		r := soundRule{left: left, match: match, right: right, sounds: parsePronunciation(sounds)}
		rules[match[0]-'A'] = append(rules[match[0]-'A'], r)
	}
	return
})

func isVowelLetter(c byte) bool {
	return c == 'A' || c == 'E' || c == 'I' || c == 'O' || c == 'U' || c == 'Y'
}

func isConsonantLetter(c byte) bool {
	return c >= 'A' && c <= 'Z' && !isVowelLetter(c)
}

// matchRight reports whether text, read forward from position i, fits the
// right context pattern.
func matchRight(pattern, text string, i int) bool {
	at := func(j int) byte {
		if j < len(text) {
			return text[j]
		}
		return '_'
	}
	for p := 0; p < len(pattern); p++ {
		switch pattern[p] {
		case '#':
			if !isVowelLetter(at(i)) {
				return false
			}
			for isVowelLetter(at(i)) {
				i++
			}
		case ':':
			for isConsonantLetter(at(i)) {
				i++
			}
		case '^':
			if !isConsonantLetter(at(i)) {
				return false
			}
			i++
		case '.':
			if !strings.ContainsRune("BDVGJLMNRWZ", rune(at(i))) {
				return false
			}
			i++
		case '+':
			if !strings.ContainsRune("EIY", rune(at(i))) {
				return false
			}
			i++
		case '%':
			switch {
			case at(i) == 'E' && at(i+1) == 'L' && at(i+2) == 'Y':
				i += 3
			case at(i) == 'E' && strings.ContainsRune("RSD", rune(at(i+1))):
				i += 2
			case at(i) == 'E':
				i++
			case at(i) == 'I' && at(i+1) == 'N' && at(i+2) == 'G':
				i += 3
			default:
				return false
			}
		default:
			if at(i) != pattern[p] {
				return false
			}
			i++
		}
	}
	return true
}

// matchLeft reports whether text, read backward from position i, fits the
// left context pattern.
func matchLeft(pattern, text string, i int) bool {
	at := func(j int) byte {
		if j >= 0 {
			return text[j]
		}
		return '_'
	}
	for p := len(pattern) - 1; p >= 0; p-- {
		switch pattern[p] {
		case '#':
			if !isVowelLetter(at(i)) {
				return false
			}
			for isVowelLetter(at(i)) {
				i--
			}
		case ':':
			for isConsonantLetter(at(i)) {
				i--
			}
		case '^':
			if !isConsonantLetter(at(i)) {
				return false
			}
			i--
		case '.':
			if !strings.ContainsRune("BDVGJLMNRWZ", rune(at(i))) {
				return false
			}
			i--
		case '+':
			if !strings.ContainsRune("EIY", rune(at(i))) {
				return false
			}
			i--
		case '&':
			switch {
			case at(i) == 'H' && (at(i-1) == 'C' || at(i-1) == 'S'):
				i -= 2
			case strings.ContainsRune("SCGZXJ", rune(at(i))):
				i--
			default:
				return false
			}
		case '@':
			switch {
			case at(i) == 'H' && strings.ContainsRune("TCS", rune(at(i-1))):
				i -= 2
			case strings.ContainsRune("TSRDLZNJ", rune(at(i))):
				i--
			default:
				return false
			}
		default:
			if at(i) != pattern[p] {
				return false
			}
			i--
		}
	}
	return true
}

// soundOut applies the letter-to-sound rules to one word of capital
// letters and apostrophes.
func soundOut(word string) pronunciation {
	rules := soundRules()
	text := "_" + word + "_"
	var pr pronunciation
	for i := 1; i < len(text)-1; {
		c := text[i]
		if c < 'A' || c > 'Z' {
			i++
			continue
		}
		applied := false
		for _, r := range rules[c-'A'] {
			if !strings.HasPrefix(text[i:], r.match) {
				continue
			}
			if !matchLeft(r.left, text, i-1) || !matchRight(r.right, text, i+len(r.match)) {
				continue
			}
			pr = append(pr, r.sounds...)
			i += len(r.match)
			applied = true
			break
		}
		if !applied {
			i++
		}
	}
	return easeOnset(pr)
}

// easeOnset puts a neutral vowel between two consonants that start a
// word and that English does not allow there: "KSHEE" is said "kuh-shee".
// A consonant can be followed by a liquid or a glide, and S can be
// followed by any consonant.
func easeOnset(pr pronunciation) pronunciation {
	if len(pr) < 2 || pr[0].isVowel() || pr[1].isVowel() || pr[0] == phS ||
		pr[1] == phL || pr[1] == phR || pr[1] == phW || pr[1] == phY {
		return pr
	}
	return slices.Insert(pr, 1, phAX)
}

// letterNames gives the pronunciation of each letter when it is said as a
// letter ("I L S", "D M E").
var letterNames = func() (names [26]pronunciation) {
	for i, s := range []string{
		"EY", "B IY", "S IY", "D IY", "IY", "EH F", "JH IY", "EY CH", "AY", "JH EY", "K EY", "EH L", "EH M",
		"EH N", "OW", "P IY", "K Y UW", "AA R", "EH S", "T IY", "Y UW", "V IY", "D AH B AX L Y UW", "EH K S",
		"W AY", "Z IY",
	} {
		names[i] = parsePronunciation(s)
	}
	return
}()

// spellOut pronounces a word letter by letter.
func spellOut(word string) pronunciation {
	var pr pronunciation
	for _, c := range strings.ToUpper(word) {
		if c >= 'A' && c <= 'Z' {
			pr = append(pr, letterNames[c-'A']...)
		}
	}
	return pr
}

// pronunciationCache memoizes pronounce: the same words recur in every
// transmission.
var pronunciationCache sync.Map // string -> pronunciation

// pronounce returns the phonemes of a word. A word in the lexicon has its
// listed pronunciation; a word with no vowel letters can only have been
// spelled out; anything else is sounded out by rule. Digits are spoken
// one at a time.
func pronounce(word string) pronunciation {
	word = strings.ToLower(word)
	if pr, ok := pronunciationCache.Load(word); ok {
		return pr.(pronunciation)
	}
	pr := pronounceUncached(word)
	pronunciationCache.Store(word, pr)
	return pr
}

func pronounceUncached(word string) pronunciation {
	if pr, ok := lexicon()[word]; ok {
		return pr
	}

	var pr pronunciation
	var letters strings.Builder
	flush := func() {
		if letters.Len() == 0 {
			return
		}
		w := letters.String()
		letters.Reset()
		if lp, ok := lexicon()[strings.ToLower(w)]; ok {
			pr = append(pr, lp...)
		} else if !strings.ContainsAny(w, "AEIOUY") || len(w) == 1 {
			pr = append(pr, spellOut(w)...)
		} else if head, tail, ok := splitCompound(w); ok {
			pr = append(pr, soundOut(head)...)
			pr = append(pr, tail...)
		} else {
			pr = append(pr, soundOut(w)...)
		}
	}
	for _, c := range strings.ToUpper(word) {
		switch {
		case c >= 'A' && c <= 'Z' || c == '\'':
			letters.WriteRune(c)
		case c >= '0' && c <= '9':
			flush()
			pr = append(pr, lexicon()[digitNames[c-'0']]...)
		default:
			flush()
		}
	}
	flush()
	return pr
}

const (
	// compoundTail is the least number of letters of a word of the lexicon
	// at the end of a longer word for the longer word to be read as a
	// compound of the two.
	compoundTail = 5
	// compoundHead is the least number of letters before it.
	compoundHead = 3
)

// splitCompound splits a word of capital letters that the recognizer ran
// together ("COUNTERTOWER") before the longest word of the lexicon that
// ends it. It returns the letters before that word and its pronunciation.
func splitCompound(w string) (head string, tail pronunciation, ok bool) {
	for i := compoundHead; i+compoundTail <= len(w); i++ {
		if tail, ok := lexicon()[strings.ToLower(w[i:])]; ok {
			return w[:i], tail, true
		}
	}
	return "", nil, false
}

// pronouncePhrase returns the phonemes of a sequence of words spoken
// together.
func pronouncePhrase(words ...string) pronunciation {
	var pr pronunciation
	for _, w := range words {
		for part := range strings.FieldsSeq(w) {
			pr = append(pr, pronounce(part)...)
		}
	}
	return pr
}

var digitNames = [10]string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}
