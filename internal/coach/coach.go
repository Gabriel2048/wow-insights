// Package coach turns the findings the analysis computed into the sentences a
// player reads, optionally by asking a language model to word them.
//
// **The model may not say anything the analysis did not already find.** That
// is the whole design, and everything in this package exists to enforce it.
// The analysis produces a fixed list of findings, each with a RuleID and the
// arithmetic behind it; the model is handed that list and a fact sheet about
// the pull, and may reword each finding, order them, and set one aside with a
// reason. It gets back exactly the findings it was given, because the reply is
// matched to them by reference before anything is rendered, and because every
// number and every proper noun in its prose is checked against the evidence
// that finding carries. There is no field in the reply for a finding it
// invented and no path by which one reaches the page.
//
// The reason for all of this is a thing that happened while #2 was being
// researched. Three search engines were asked whether Warcraft Logs had an "AI
// Analyze" feature; all three said yes, confidently, citing a real URL titled
// "AI Analyze | Warcraft Logs". The API says guild 6174 is a guild *named*
// "AI" and the page is the ordinary guild Analyze page. A summarising layer
// invented a product out of three guild names, three times over, with total
// confidence. On a coaching page a wrong claim — "you missed 4 Hot Streaks" —
// is disprovable in one click, and it takes the deterministic half of the page
// down with it.
//
// So the split is: the analysis decides *what is true*, and this package
// decides *how it reads*. When the model is absent, misconfigured, slow,
// refusing, or wrong, the page shows the analysis's own words. That is a
// worse-written page, never a wrong one.
package coach

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"wowinsight/internal/knowledge"
	"wowinsight/internal/warcraftlogs"
)

// Written is one finding as the page should read it: the finding itself,
// unchanged, carrying whichever words were chosen for it.
//
// The embedding is what makes the guarantee structural rather than a rule
// somebody has to remember. Title and Detail are the *Finding's* fields, so
// wording a finding is assigning to a copy of one, and a Written cannot be
// built except from a Finding this process computed. Severity, At, RuleID and
// Evidence are reached through the same embedding and are never rewritten —
// the claim may be reworded, the working behind it may not.
type Written struct {
	warcraftlogs.Finding

	// SetAside marks a finding the model judged the player had a reason for.
	// It is still shown, below the rest and with Why next to it: a coaching
	// page that silently drops a finding is one nobody can check, and the
	// reason is the most interesting thing on the page when it is right.
	SetAside bool
	// Why is the model's reason for setting it aside, held to the same rules
	// as the rest of its prose.
	Why string
}

// Findings is the page's list. It is a named type so the template can ask it
// which findings stand and which were set aside without a helper function
// living in internal/web to answer that.
type Findings []Written

// Standing are the findings the player should act on.
func (fs Findings) Standing() []Written { return fs.where(false) }

// SetAside are the findings the model judged the player had a reason for.
// They are still on the page, below the rest: a coaching page that silently
// drops a finding is one nobody can check, and when the judgement is right
// the reason is the most useful sentence on the page.
func (fs Findings) SetAside() []Written { return fs.where(true) }

func (fs Findings) where(aside bool) []Written {
	var out []Written
	for _, f := range fs {
		if f.SetAside == aside {
			out = append(out, f)
		}
	}
	return out
}

// Deterministic presents findings in the analysis's own words. It is what the
// page shows when there is no model configured, when the model failed, and
// when anything it said did not survive validation.
func Deterministic(found []warcraftlogs.Finding) Findings {
	out := make(Findings, len(found))
	for i, f := range found {
		out[i] = Written{Finding: f}
	}
	return out
}

// The dials of one request.
const (
	// model is the one this page is written for. Wording a finding well is a
	// judgement about a raid pull, not a transformation, and the cheaper
	// models are measurably worse at deciding that a Combustion held for an
	// intermission was held on purpose — which is the one judgement this
	// whole layer exists to make.
	model = "claude-opus-5"

	// maxTokens bounds one reply. The prose itself is a few hundred tokens;
	// the headroom is for the thinking that precedes it, which counts against
	// the same ceiling. A reply that hits the cap is truncated mid-JSON and
	// fails to decode, which lands on the same degrade path as any other
	// failure — wasteful, never wrong.
	maxTokens = 16000

	// requestTimeout bounds one call from this side. It sits well inside
	// jobDeadline in internal/web, which is what actually bounds the job;
	// this is here so a hung connection cannot occupy a job slot for ten
	// minutes.
	requestTimeout = 2 * time.Minute
)

// Writer words findings with a language model.
type Writer struct {
	api anthropic.Client
	log *slog.Logger

	// Set by options and read by New, which builds the client from them. The
	// SDK's client is immutable once constructed, so an option cannot reach
	// into it the way warcraftlogs.Option reaches into *Client; collecting
	// the choices here and building last is the same pattern adapted to that.
	transport http.RoundTripper
}

// Option configures a Writer, in the shape internal/warcraftlogs already uses:
// Go has no optional parameters, so this is how New takes anything past the
// key.
type Option func(*Writer)

// WithTransport replaces the transport under the API request. It is the same
// seam warcraftlogs.WithTransport is, and it is here for the same reason: a
// test, and one day a recording, replaces the wire without the code above it
// knowing. A nil transport is ignored rather than installed.
func WithTransport(rt http.RoundTripper) Option {
	return func(w *Writer) {
		if rt != nil {
			w.transport = rt
		}
	}
}

// New returns a Writer that talks to the Anthropic API with the given key.
//
// The key goes to the SDK and is never kept on the struct. This process
// already refuses to put a Warcraft Logs secret anywhere but the one request
// that needs it, and a field holding an API key is one %+v away from a log
// line.
func New(key string, logger *slog.Logger, opts ...Option) *Writer {
	w := &Writer{log: logger, transport: http.DefaultTransport}
	for _, opt := range opts {
		opt(w)
	}
	req := []option.RequestOption{
		option.WithAPIKey(key),
		// The SDK's own advice: give it an *http.Client carrying a custom
		// RoundTripper rather than implementing its HTTPClient interface.
		// The timeout stays whatever transport is installed, so a replay that
		// hangs is bounded the same as the real wire.
		option.WithHTTPClient(&http.Client{Timeout: requestTimeout, Transport: w.transport}),
	}
	w.api = anthropic.NewClient(req...)
	return w
}

// Input is everything one call needs. It is a struct because the list is long
// enough that positional arguments would be a guessing game, and because the
// roster on Detail is what the outbound body is checked against.
type Input struct {
	Detail   *warcraftlogs.FightDetail
	Player   warcraftlogs.PlayerStats
	Timeline *warcraftlogs.Timeline
	Know     knowledge.Knowledge
	Findings []warcraftlogs.Finding
}

// Write returns the findings as the page should read them.
//
// It returns the deterministic wording alongside every error, so a caller can
// render the page and mention what did not happen rather than choosing between
// the two. Nothing here is worth failing a page over: the findings are already
// computed and already true.
func (w *Writer) Write(ctx context.Context, in Input) (Findings, error) {
	plain := Deterministic(in.Findings)
	if len(in.Findings) == 0 {
		// Nothing to word, so nothing to spend and nothing to risk. This is
		// the common case on a pull played well, and it is also the one case
		// where a model with nothing to do would be most tempted to find
		// something — so it is not asked.
		return plain, nil
	}

	facts, err := buildFacts(in)
	if err != nil {
		return plain, err
	}
	body, err := facts.marshal()
	if err != nil {
		return plain, err
	}
	// Fail closed. Nothing leaves this process until the roster of this very
	// fight has been checked against what is about to go out.
	if err := identityOf(in).check(body, in.Findings); err != nil {
		return plain, err
	}

	reply, err := w.ask(ctx, in, body)
	if err != nil {
		return plain, err
	}
	written, err := validate(in, facts, reply)
	if err != nil {
		return plain, err
	}
	return written, nil
}

// ask makes the one request and decodes the reply into blocks.
func (w *Writer) ask(ctx context.Context, in Input, facts []byte) ([]block, error) {
	refs := make([]string, len(in.Findings))
	seen := make(map[string]bool, len(in.Findings))
	for i, f := range in.Findings {
		refs[i] = ref(f)
		if seen[refs[i]] {
			// Two findings this process cannot tell apart. validate() catches
			// this as well, but only after a request whose answer could never
			// have been used — and the clash reaches the wire as a duplicate
			// inside an enum, which is this app shipping nonsense.
			return nil, fmt.Errorf("%w: two findings share the reference %q", ErrUntrustworthy, refs[i])
		}
		seen[refs[i]] = true
	}
	schema, err := replySchema(refs)
	if err != nil {
		return nil, err
	}
	msg, err := w.api.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     model,
		MaxTokens: maxTokens,
		System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
		// Adaptive thinking, because the one judgement asked for here — was
		// this cooldown held on purpose? — is the kind that wants reasoning
		// and the kind a confident guess gets wrong.
		Thinking: anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}},
		OutputConfig: anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(userPrompt(in, facts))),
		},
	})
	if err != nil {
		return nil, wireError(err)
	}
	// A refusal is an HTTP 200 with no usable content, so stop_reason is asked
	// before the content is read. There is nothing here worth refusing and no
	// server-side fallback configured; if one ever fires, it degrades exactly
	// as an outage does, with the category in the log.
	if msg.StopReason == anthropic.StopReasonRefusal {
		return nil, fmt.Errorf("%w: the model declined (%s)", ErrDeclined, msg.StopDetails.Category)
	}
	// What it cost, at the one place that knows. This is the only spend this
	// app makes that is not measured in Warcraft Logs points, and a page
	// nobody is watching the price of is a page that gets switched off in a
	// hurry one month.
	w.log.Info("wrote up findings",
		"findings", len(in.Findings),
		"fight", in.Timeline.Subject.FightID,
		"input_tokens", msg.Usage.InputTokens,
		"output_tokens", msg.Usage.OutputTokens,
		"stop_reason", msg.StopReason)
	return decodeReply(msg)
}

// wireError maps what the SDK returned onto the sentinel a caller can act on,
// so that an Anthropic failure is never reported as Warcraft Logs failing.
// internal/web's classify has exactly one fallback row and it names the wrong
// vendor for everything that reaches it.
func wireError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == http.StatusBadRequest && strings.Contains(apiErr.RawJSON(), "credit balance"):
			// An empty account answers 400 invalid_request_error, the same
			// shape a genuinely malformed request gets, so the body is what
			// separates them. Matching on the API's own sentence is fragile
			// by nature; when it changes this falls through to
			// ErrModelUnavailable, which is wrong but safe.
			return fmt.Errorf("%w: %w", ErrNoCredit, err)
		case apiErr.StatusCode == http.StatusTooManyRequests:
			return fmt.Errorf("%w: %w", ErrModelBusy, err)
		case apiErr.StatusCode == http.StatusUnauthorized, apiErr.StatusCode == http.StatusForbidden:
			return fmt.Errorf("%w: %w", ErrBadKey, err)
		case apiErr.StatusCode >= 500:
			return fmt.Errorf("%w: %w", ErrModelBusy, err)
		}
	}
	return fmt.Errorf("%w: %w", ErrModelUnavailable, err)
}
