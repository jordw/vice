// speech/stt/lexicon.go
// Copyright(c) vice contributors, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package stt

import (
	"strconv"
	"strings"
	"sync"
)

// pronunciations lists words whose pronunciation the letter-to-sound rules
// do not derive: the number words, the radiotelephony alphabet,
// abbreviations that are said as letters, and the irregular words of the
// phraseology. Each line is a word followed by its ARPAbet phonemes.
const pronunciations = `
zero       Z IY R OW
one        W AH N
two        T UW
three      TH R IY
four       F AO R
five       F AY V
six        S IH K S
seven      S EH V AX N
eight      EY T
nine       N AY N
niner      N AY N AX R
fife       F AY F
fower      F OW AX R
ten        T EH N
eleven     IH L EH V AX N
twelve     T W EH L V
thirteen   TH AX R T IY N
fourteen   F AO R T IY N
fifteen    F IH F T IY N
sixteen    S IH K S T IY N
seventeen  S EH V AX N T IY N
eighteen   EY T IY N
nineteen   N AY N T IY N
twenty     T W EH N T IY
thirty     TH AX R T IY
forty      F AO R T IY
fourty     F AO R T IY
fifty      F IH F T IY
sixty      S IH K S T IY
seventy    S EH V AX N T IY
eighty     EY T IY
ninety     N AY N T IY
hundred    HH AH N D R AX D
thousand   TH AW Z AX N D
triple     T R IH P AX L
double     D AH B AX L

alpha      AE L F AX
alfa       AE L F AX
bravo      B R AA V OW
charlie    CH AA R L IY
delta      D EH L T AX
echo       EH K OW
foxtrot    F AA K S T R AA T
golf       G AA L F
hotel      HH OW T EH L
india      IH N D IY AX
juliet     JH UW L IY EH T
juliett    JH UW L IY EH T
kilo       K IY L OW
lima       L IY M AX
mike       M AY K
november   N OW V EH M B AX R
oscar      AA S K AX R
papa       P AA P AA
quebec     K AX B EH K
romeo      R OW M IY OW
sierra     S IY EH R AX
tango      T AE NG G OW
uniform    Y UW N AX F AO R M
victor     V IH K T AX R
whiskey    W IH S K IY
xray       EH K S R EY
yankee     Y AE NG K IY
zulu       Z UW L UW

ils        AY EH L EH S
rnav       AA R N AE V
vor        V IY OW AA R
lda        EH L D IY EY
gls        JH IY EH L EH S
gps        JH IY P IY EH S
dme        D IY EH M IY
vfr        V IY EH F AA R
ifr        AY EH F AA R
atis       EY T AX S
sid        S IH D
tacan      T AE K AE N

climb      K L AY M
climbing   K L AY M IH NG
climbed    K L AY M D
descend    D IH S EH N D
descending D IH S EH N D IH NG
descent    D IH S EH N T
maintain   M EY N T EY N
altitude   AE L T AX T UW D
heading    HH EH D IH NG
turn       T AX R N
right      R AY T
speed      S P IY D
reduce     R IH D UW S
increase   IH N K R IY S
knots      N AA T S
knot       N AA T
direct     D AX R EH K T
proceed    P R OW S IY D
cleared    K L IH R D
clear      K L IH R
approach   AX P R OW CH
expect     IH K S P EH K T
runway     R AH N W EY
localizer  L OW K AX L AY Z AX R
intercept  IH N T AX R S EH P T
contact    K AA N T AE K T
tower      T AW AX R
departure  D IH P AA R CH AX R
depart     D IH P AA R T
center     S EH N T AX R
radar      R EY D AA R
squawk     S K W AO K
ident      AY D EH N T
visual     V IH ZH UW AX L
final      F AY N AX L
minimum    M IH N AX M AX M
maximum    M AE K S AX M AX M
flight     F L AY T
level      L EH V AX L
fly        F L AY
present    P R EH Z AX N T
via        V AY AX
arrival    AX R AY V AX L
traffic    T R AE F IH K
o'clock    AX K L AA K
oclock     AX K L AA K
miles      M AY L Z
mile       M AY L
caution    K AO SH AX N
wake       W EY K
turbulence T AX R B Y AX L AX N S
information IH N F AX R M EY SH AX N
airport    EH R P AO R T
field      F IY L D
sight      S AY T
report     R IH P AO R T
until      AX N T IH L
established IH S T AE B L IH SH T
expedite   EH K S P AX D AY T
resume     R IH Z UW M
cancel     K AE N S AX L
clearance  K L IH R AX N S
restrictions R IH S T R IH K SH AX N Z
restriction R IH S T R IH K SH AX N
greater    G R EY T AX R
exceed     IH K S IY D
mach       M AA K
point      P OY N T
say        S EY
roger      R AA JH AX R
heavy      HH EH V IY
super      S UW P AX R
negative   N EH G AX T IH V
correction K AX R EH K SH AX N
disregard  D IH S R IH G AA R D
standby    S T AE N D B AY
frequency  F R IY K W AX N S IY
approved   AX P R UW V D
services   S AX R V AX S AX Z
terminated T AX R M AX N EY T AX D
navigation N AE V AX G EY SH AX N
radial     R EY D IY AX L
bearing    B EH R IH NG
course     K AO R S
join       JH OY N
vectors    V EH K T AX R Z
vector     V EH K T AX R
sequence   S IY K W AX N S
separation S EH P AX R EY SH AX N
practical  P R AE K T IH K AX L
discretion D IH S K R EH SH AX N
straight   S T R EY T
transponder T R AE N S P AA N D AX R
request    R IH K W EH S T
ahead      AX HH EH D
unable     AH N EY B AX L
again      AX G EH N
published  P AH B L IH SH T
airway     EH R W EY
degrees    D IH G R IY Z
degree     D IH G R IY
advised    AX D V AY Z D
further    F AX R DH AX R
notice     N OW T AX S
parallel   P EH R AX L EH L
landing    L AE N D IH NG
affirmative AX F AX R M AX T IH V
company    K AH M P AX N IY
immediately IH M IY D IY AX T L IY
altimeter  AE L T IH M AX T AX R
identified AY D EH N T AX F AY D

the        DH AX
a          AX
to         T UW
too        T UW
for        F AO R
of         AH V
or         AO R
are        AA R
your       Y AO R
you        Y UW
you're     Y AO R
have       HH AE V
is         IH Z
was        W AH Z
with       W IH TH
do         D UW
not        N AA T
now        N AW
own        OW N
good       G UH D
day        D EY
hello      HH AX L OW
celtic     K EH L T IH K
gucci      G UW CH IY
river      R IH V AX R
tau        T AW
isle       AY L
isles      AY L Z
aisle      AY L
aisles     AY L Z
minor      M AY N AX R
idol       AY D AX L
idols      AY D AX L Z
laser      L EY Z AX R
tiger      T AY G AX R
pilot      P AY L AX T
our        AW AX R
hour       AW AX R
`

// lexicon returns the pronunciation dictionary built from pronunciations.
var lexicon = sync.OnceValue(func() map[string]pronunciation {
	lex := make(map[string]pronunciation)
	for line := range strings.SplitSeq(pronunciations, "\n") {
		word, sounds, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		lex[word] = parsePronunciation(sounds)
	}
	return lex
})

// confusions lists hearings of the recognizer that sound nothing like
// what was said, so that no measure of sound can recover them. Each line
// is what the recognizer writes, the words it stands for, and how much
// the hearing counts as those words (as a similarity).
const confusions = `
and         = one               0.85
on          = one               0.85
it          = eight             0.85
fine        = five              0.85
fire        = five              0.85
fight       = five              0.85
tarom       = tower             0.85
countertower = contact tower    0.85
procedure   = proceed direct    0.85
tarot       = tower             0.85
your        = or                0.85
rate        = radar             0.85
read        = radar             0.85
reader      = radar             0.85
rare        = radar             0.85
wait        = radar             0.85
rate of     = radar             0.85
right of    = radar             0.85
route of    = radar             0.85
reduce to   = radar             0.85
fighting    = fly heading       0.85
sediment    = descend maintain  0.85
centimeter  = descend maintain  0.85
decimate    = descend maintain  0.85
disseminate = descend maintain  0.85
disassembly = descend via       0.85
me          = via               0.85
right a     = via               0.85
setup       = descend           0.85
at the set  = descend           0.85
club        = climb             0.85
con         = climb             0.85
secured     = cleared           0.85
clicked     = cleared           0.85
effect      = expect            0.85
isle        = ils               0.85
rls         = ils               0.85
terminals   = turbulence        0.85
route       = right direct      0.85
rioted      = right             0.85
righted     = right             0.85
rider       = right             0.85
rating      = right             0.85
riding      = right             0.85
time        = turn              0.85
twelve      = until             0.85
i dead      = ident             0.95
zero        = zulu              0.80
explained   = expedite          0.85
will        = level             0.85
to recall   = direct            0.85
to park     = depart            0.85
accept      = intercept         0.85
insert      = intercept         0.85
`

// confusionTable maps a heard word to the words it can stand for.
var confusionTable = sync.OnceValue(func() map[similarityKey]float32 {
	table := make(map[similarityKey]float32)
	for line := range strings.SplitSeq(confusions, "\n") {
		heard, rest, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		quality, err := strconv.ParseFloat(fields[len(fields)-1], 32)
		if err != nil {
			panic("stt: confusion " + line + ": " + err.Error())
		}
		key := similarityKey{heard: strings.TrimSpace(heard), target: strings.Join(fields[:len(fields)-1], " ")}
		table[key] = float32(quality)
	}
	return table
})

// confusionQuality returns the listed quality of hearing one word for
// another, or 0 if the pair is not listed.
func confusionQuality(heard, target string) float32 {
	return confusionTable()[similarityKey{heard: heard, target: target}]
}
