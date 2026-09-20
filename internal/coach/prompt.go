package coach

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"wowinsight/internal/warcraftlogs"
)

// systemPrompt is the job. It is short on purpose: every extra instruction is
// another thing that can be followed instead of the two that matter, which
// are "say nothing the evidence does not say" and "these are the findings,
// all of them, and there are no others".
//
// It is written as permissions and prohibitions rather than as a persona. A
// persona is a thing a model can decide it is being, and the failure this
// whole package exists to prevent — an authoritative sentence about something
// that did not happen — is exactly what a confident persona produces.
const systemPrompt = `You are helping a World of Warcraft raider read their own combat log.

You are given a fixed list of findings about one pull. It is the whole list: you will not
be given others, and you may not produce any. Each carries the arithmetic behind it, which
the player sees printed directly underneath your sentence. Your job is to word them, and to
judge whether the pull gives the player a good reason for one of them.

For each finding you are given, return exactly one block, keyed by its "ref".

You may:
  - rewrite its title and detail in clearer, plainer language;
  - return the blocks in the order a player should read them;
  - set a finding aside, with a reason, when the fact sheet shows the player had a good
    reason to do what the finding is about. Holding a cooldown for an intermission, for a
    damage amplifier, for Bloodlust, or for a mechanic that is about to happen, is playing
    well and not a mistake. A set-aside finding is still shown to the player, below the
    others, with your reason next to it.

You may not:
  - state any number that is not in that finding's own evidence;
  - name any spell, ability, aura, phase, boss or mechanic that does not appear in the fact
    sheet or in the finding you are rewording;
  - add a finding, a recommendation, an observation, or a compliment of your own. The fact
    sheet is there for you to judge the findings against. It is not a source of things to
    say.

How to write:
  - Answer three questions, in this order: what happened, why it costs them, and what to do
    instead. Name a moment they can go and look at rather than an average.
  - Write numbers as digits, with the units the evidence uses: 37s, 4:53, 2 uses. Never
    write a number as a word. Never write a proportion — no "half", "a third", "most of".
  - Second person, present tense, no preamble, no headings, no markdown.
  - A title is a short line. A detail is one or two sentences.

A number the player cannot find in the evidence printed below your sentence costs this page
its credibility, and it takes the analyser's correct findings down with it. When you are
unsure whether you may say something, keep the wording you were given.`

// replySchema constrains the answer's shape. It is the structural half of the
// guarantee: there is one object per finding, the fields are fixed, and
// additionalProperties is false, so there is no slot anywhere for a finding
// the model made up. The rest of the guarantee is validate(), which checks
// that the refs are the ones handed out and that the prose says nothing new.
var replySchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"findings": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ref":       map[string]any{"type": "string", "description": "the ref of the finding this block rewords, copied exactly"},
					"title":     map[string]any{"type": "string", "description": "the claim, in a short line"},
					"detail":    map[string]any{"type": "string", "description": "the claim in one or two sentences, with its numbers in it"},
					"set_aside": map[string]any{"type": "boolean", "description": "true when the pull gives the player a good reason for this"},
					"why":       map[string]any{"type": "string", "description": "why it is set aside; empty when it is not"},
				},
				"required":             []string{"ref", "title", "detail", "set_aside", "why"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"findings"},
	"additionalProperties": false,
}

// block is one finding as the model worded it.
type block struct {
	Ref      string `json:"ref"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	SetAside bool   `json:"set_aside"`
	Why      string `json:"why"`
}

// reply is what comes back.
type reply struct {
	Findings []block `json:"findings"`
}

// ref names one finding in one pull.
//
// RuleID alone is not enough and it is worth saying why, because the issue
// this was built from says it is. RuleID names the *rule*, not the instance:
// a spec with two judged cooldowns produces two findings from
// "cooldown-unused-tail", and a reply keyed on the rule could attach the
// wording of one to the other. The moment disambiguates them, and it is still
// derived entirely from a finding this process computed — so there is still
// nowhere to put an invented one.
//
// An index would also be unambiguous and is worse: an off-by-one in an index
// silently attaches a sentence about Combustion to a finding about something
// else and reads perfectly. A ref that is wrong is simply not found.
func ref(f warcraftlogs.Finding) string { return f.RuleID + "@" + f.Timestamp() }

// userPrompt is the pull and the findings.
func userPrompt(in Input, sheet []byte) string {
	var b strings.Builder
	b.WriteString("Here is the pull.\n\n<fact_sheet>\n")
	b.Write(sheet)
	b.WriteString("\n</fact_sheet>\n\n")
	fmt.Fprintf(&b, "Here %s to word, and you must return %s.\n\n<findings>\n",
		countOf(len(in.Findings), "is", "are", "finding"), countOf(len(in.Findings), "", "", "block"))
	for _, f := range in.Findings {
		fmt.Fprintf(&b, "ref: %s\n", ref(f))
		fmt.Fprintf(&b, "severity: %s\n", f.Severity)
		fmt.Fprintf(&b, "at: %s\n", f.Timestamp())
		fmt.Fprintf(&b, "title: %s\n", f.Title)
		fmt.Fprintf(&b, "detail: %s\n", f.Detail)
		b.WriteString("evidence the player will see under your sentence:\n")
		for _, e := range f.Evidence {
			fmt.Fprintf(&b, "  - %s: %s\n", e.Label, e.Value)
		}
		b.WriteString("\n")
	}
	b.WriteString("</findings>\n")
	return b.String()
}

// countOf writes "is 1 finding" or "are 3 findings". The prompt asks the model
// to be careful with number and unit, so the sentence doing the asking should
// not read "There are 1 of them and you must return 1 blocks."
func countOf(n int, singular, plural, noun string) string {
	verb, s := singular, ""
	if n != 1 {
		verb, s = plural, "s"
	}
	if verb != "" {
		verb += " "
	}
	return fmt.Sprintf("%s%d %s%s", verb, n, noun, s)
}

// decodeReply pulls the structured answer out of the response.
func decodeReply(msg *anthropic.Message) ([]block, error) {
	var text strings.Builder
	for _, c := range msg.Content {
		if t, ok := c.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		// A reply that ran out of room before it said anything, most often.
		return nil, fmt.Errorf("%w: the reply carried no answer (stop reason %q)", ErrModelUnavailable, msg.StopReason)
	}
	var out reply
	if err := json.Unmarshal([]byte(text.String()), &out); err != nil {
		return nil, fmt.Errorf("%w: the reply was not the shape asked for: %w", ErrUntrustworthy, err)
	}
	return out.Findings, nil
}
