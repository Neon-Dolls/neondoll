// Package actionpolicy evaluates Owner/Primary-Peer restrictions on an action
// before Body discovery. It is separate from per-Body Guard authorization.
package actionpolicy

import (
	"errors"
	"fmt"
	"strings"
)

type Decision string

const (
	DecisionAllow           Decision = "allow"
	DecisionDeny            Decision = "deny"
	DecisionRequireApproval Decision = "require_approval"
)

var ErrInvalidPolicy = errors.New("invalid action policy")

type Request struct {
	Doll       string
	Capability string
	Operation  string
	Arguments  map[string]any
}

type Result struct {
	Decision Decision
	Reason   string
	Message  string
}

type Rule struct {
	Doll       string   `json:"doll,omitempty"`
	Capability string   `json:"capability"`
	Operation  string   `json:"operation"`
	Decision   Decision `json:"decision"`
	Reason     string   `json:"reason,omitempty"`
	Message    string   `json:"message,omitempty"`
}

type Policy interface {
	Evaluate(Request) Result
}

// Evaluator applies exact configured rules and defaults to allow. Empty Doll
// on a rule applies to the Core's Doll; a non-empty Doll scopes the rule.
type Evaluator struct{ rules []Rule }

func New(rules []Rule) (*Evaluator, error) {
	seen := make(map[string]struct{}, len(rules))
	copyRules := make([]Rule, len(rules))
	for index, rule := range rules {
		if strings.TrimSpace(rule.Capability) == "" || strings.TrimSpace(rule.Operation) == "" ||
			(rule.Decision != DecisionAllow && rule.Decision != DecisionDeny && rule.Decision != DecisionRequireApproval) {
			return nil, fmt.Errorf("%w: rule %d", ErrInvalidPolicy, index)
		}
		key := rule.Doll + "\x00" + rule.Capability + "\x00" + rule.Operation
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate rule %d", ErrInvalidPolicy, index)
		}
		seen[key] = struct{}{}
		copyRules[index] = rule
	}
	return &Evaluator{rules: copyRules}, nil
}

func Default() *Evaluator { return &Evaluator{} }

func (evaluator *Evaluator) Evaluate(request Request) Result {
	var matched *Result
	for _, rule := range evaluator.rules {
		if rule.Capability == request.Capability && rule.Operation == request.Operation &&
			(rule.Doll == "" || rule.Doll == request.Doll) {
			result := Result{Decision: rule.Decision, Reason: rule.Reason, Message: rule.Message}
			if rule.Decision == DecisionDeny {
				return result
			}
			if matched == nil || rule.Decision == DecisionRequireApproval {
				matched = &result
			}
		}
	}
	if matched != nil {
		return *matched
	}
	return Result{Decision: DecisionAllow, Reason: "unrestricted"}
}

var _ Policy = (*Evaluator)(nil)
