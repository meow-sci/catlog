package stats

import (
	"context"
	"fmt"
)

type challengeValue func(context.Context, *Batch, Event) (float64, map[string]any, bool, error)

// challengeRule is the executable half of one compile-time Challenge.
//
// The generic engine owns only receive-window gating and scoped persistence. A
// rule that reads a flight-bearing event MUST call scoreable before returning a
// contribution. Keeping that call in the value function makes the eligibility
// decision visible beside the rule that selects the event; the engine must not
// pretend every event has a flight or silently score one it did not inspect.
type challengeRule struct {
	kind  statKind
	value challengeValue
}

// H3 ships the generic fold shape but no rules. H4 adds one entry here for each
// Challenge literal in challengeCatalogue.
var challengeRules = map[string]challengeRule{}

// challengeFold is one challenge's rule. The receive-time gate deliberately
// runs before the arbitrary value function.
type challengeFold struct {
	c     Challenge
	kind  statKind
	value challengeValue
}

func (f challengeFold) Name() string { return challengeFoldPrefix + f.c.Key }

func (f challengeFold) Apply(ctx context.Context, b *Batch, ev Event) error {
	if ev.RecvTime <= 0 || !f.c.InWindow(ev.RecvTime) {
		return nil
	}
	value, cx, ok, err := f.value(ctx, b, ev)
	if err != nil || !ok {
		return err
	}
	return putChallenge(ctx, b, ev, f.c, f.kind, value, cx)
}

// ChallengeFolds returns one stable challenge:<key> fold per registry item.
// Construction is validated and panics on compile-time registry corruption,
// matching SecondPassFolds' fail-fast fold-name path.
func ChallengeFolds() []Fold {
	folds, err := challengeFoldsFor(Challenges(), challengeRules)
	if err != nil {
		panic(err)
	}
	return folds
}

func challengeFoldsFor(defs []Challenge, rules map[string]challengeRule) ([]Fold, error) {
	if len(rules) != len(defs) {
		return nil, fmt.Errorf("stats: challenge rule count %d does not match definition count %d", len(rules), len(defs))
	}
	folds := make([]Fold, 0, len(defs))
	for _, c := range defs {
		rule, ok := rules[c.Key]
		if !ok || rule.value == nil {
			return nil, fmt.Errorf("stats: challenge %q has no executable rule", c.Key)
		}
		if rule.kind != kindRecord && rule.kind != kindBest && rule.kind != kindCount {
			return nil, fmt.Errorf("stats: challenge %q has invalid write kind %d", c.Key, rule.kind)
		}
		folds = append(folds, challengeFold{c: c, kind: rule.kind, value: rule.value})
	}
	return folds, nil
}

// putChallenge writes a contribution into exactly the scope declared by c.
// It deliberately owns no eligibility rule: flight-bearing value functions
// must call scoreable explicitly before returning ok=true.
func putChallenge(ctx context.Context, b *Batch, ev Event, c Challenge, kind statKind, value float64, context map[string]any) error {
	cx, err := encodeContext(context)
	if err != nil {
		return err
	}
	k := challengeStatKey{playerID: ev.PlayerID, challenge: c.Key}
	switch c.Scope {
	case ScopePlayer:
		// Empty career and system are the genuine cross-save/system sentinel.
	case ScopeCareer, ScopeSystem:
		if ev.Career == "" {
			return nil
		}
		system, err := b.CareerSystem(ctx, ev.PlayerID, ev.Career)
		if err != nil || system == "" {
			return err
		}
		k.system = system
		if c.Scope == ScopeCareer {
			k.career = ev.Career
		}
	default:
		return fmt.Errorf("stats: challenge %q has invalid scope %q", c.Key, c.Scope)
	}
	b.putChallengeStat(kind, k, value, cx, ev.Seq)
	return nil
}

func putChallengeRecord(ctx context.Context, b *Batch, ev Event, c Challenge, value float64, cx map[string]any) error {
	return putChallenge(ctx, b, ev, c, kindRecord, value, cx)
}

func putChallengeBest(ctx context.Context, b *Batch, ev Event, c Challenge, value float64, cx map[string]any) error {
	return putChallenge(ctx, b, ev, c, kindBest, value, cx)
}

func addChallengeCount(ctx context.Context, b *Batch, ev Event, c Challenge, delta float64) error {
	return putChallenge(ctx, b, ev, c, kindCount, delta, nil)
}
