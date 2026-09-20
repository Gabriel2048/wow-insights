package coach

import (
	"bytes"
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
  - set a finding aside, when the fact sheet shows the player had a good reason to do what
    the finding is about. Holding a cooldown for an intermission, for a damage amplifier,
    for Bloodlust, or for a mechanic that is about to happen, is playing well and not a
    mistake. A set-aside finding is still shown to the player, below the others, with your
    reason next to it. Set "set_aside" true and put the reason in "set_aside_reason";
    otherwise "set_aside_reason" is an empty string. Every finding still needs a title and
    a detail, set aside or not.

You may not:
  - state any number that is not in that finding's own evidence;
  - name any spell, ability, aura, phase, boss or mechanic that does not appear in the fact
    sheet or in the finding you are rewording;
  - add a finding, a recommendation, an observation, or a compliment of your own. The fact
    sheet is there for you to judge the findings against. It is not a source of things to
    say.

How to write:
  - Put three things in the detail, in this order: what happened, what it costs them, and
    what to do instead. Name a moment they can go and look at rather than an average.
  - Write numbers as digits, with the units the evidence uses: 37s, 4:53, 2 uses. Never
    write a number as a word. Never write a proportion — no "half", "a third", "most of".
  - Second person, present tense, no preamble, no headings, no markdown.
  - A title is a short line. A detail is one or two sentences.

A number the player cannot find in the evidence printed below your sentence costs this page
its credibility, and it takes the analyser's correct findings down with it. When you are
unsure whether you may say something, keep the wording you were given.`

// replySchema constrains the answer, and is built per request because the
// strongest constraints it can carry are the ones that depend on what was
// asked.
//
// Three of them do real work that no validator can do as well, because the
// model is decoded against this and simply cannot emit anything else:
//
//   - "ref" is an enum of the references actually handed out, so there is no
//     token sequence that names a finding this process did not compute;
//   - the properties are listed in the order they should be written, which
//     is the order the model fills them in. That is why they are a
//     json.RawMessage: Go marshals a map's keys alphabetically, which put
//     "detail" first and asked for the prose before the model had committed
//     to which finding it was about.
//
// The array is deliberately unbounded: the API rejects minItems and maxItems
// on an array ("For 'array' type, property 'maxItems' is not supported"), so
// "one block per finding, no more and no fewer" is validate()'s to enforce
// and cannot be pushed into the schema.
//
// validate() re-checks the refs too. A schema is a claim about what the
// server will accept, and nothing on this page should rest on a claim made by
// the thing being checked.
func replySchema(refs []string) (map[string]any, error) {
	item, err := orderedObject(
		"ref", map[string]any{"type": "string", "enum": refs,
			"description": "the ref of the finding this block rewords, copied exactly"},
		"title", map[string]any{"type": "string",
			"description": "the claim in a short line, under about twelve words"},
		"detail", map[string]any{"type": "string",
			"description": "the claim in one or two sentences, with its numbers in it; never empty"},
		"set_aside", map[string]any{"type": "boolean",
			"description": "true only when the pull gives the player a good reason for this"},
		"set_aside_reason", map[string]any{"type": "string",
			"description": "the reason it is set aside, or an empty string when set_aside is false"},
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"findings": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"properties":           item,
					"required":             []string{"ref", "title", "detail", "set_aside", "set_aside_reason"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"findings"},
		"additionalProperties": false,
	}, nil
}

// orderedObject renders a JSON object with its keys in the order given, as
// alternating key and value. It exists because encoding/json sorts a map's
// keys, and the order a schema lists its properties in is the order a
// constrained decoder fills them in — so an alphabetical schema decides how
// the model writes.
func orderedObject(pairs ...any) (json.RawMessage, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("%w: orderedObject wants alternating key and value", ErrUntrustworthy)
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("%w: orderedObject key %d is not a string", ErrUntrustworthy, i)
		}
		value, err := json.Marshal(pairs[i+1])
		if err != nil {
			return nil, fmt.Errorf("%w: orderedObject value for %q: %w", ErrUntrustworthy, key, err)
		}
		if i > 0 {
			b.WriteByte(',')
		}
		name, err := json.Marshal(key)
		if err != nil {
			return nil, fmt.Errorf("%w: orderedObject key %q: %w", ErrUntrustworthy, key, err)
		}
		b.Write(name)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// A field here must not be named for a word the writing rules use in another
// sense. "why" was, and a real call came back with the answer to "why it
// costs them" sitting in the field meant for why a finding was set aside,
// with "detail" left empty. It is "set_aside_reason" now, and the rules say
// "what it costs them". No test can catch the next one of these — the
// collision is in meaning, not in spelling — so it is written down here.
//
// block is one finding as the model worded it.
type block struct {
	Ref      string `json:"ref"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	SetAside bool   `json:"set_aside"`
	Why      string `json:"set_aside_reason"`
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
