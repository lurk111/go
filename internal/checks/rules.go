package checks

import (
	"fmt"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
)

type CompiledRule struct {
	Spec    RuleSpec
	trigger cel.Program
	recover cel.Program
}

func CompileRules(c CheckSpec) ([]CompiledRule, error) {
	env, err := cel.NewEnv(cel.Variable("fields", cel.MapType(cel.StringType, cel.DynType)), cel.Variable("previous", cel.MapType(cel.StringType, cel.DynType)), cel.Variable("now", cel.TimestampType), cel.Variable("entity_key", cel.StringType))
	if err != nil {
		return nil, err
	}
	compile := func(text string) (cel.Program, error) {
		ast, issues := env.Compile(text)
		if issues != nil && issues.Err() != nil {
			return nil, issues.Err()
		}
		if ast.OutputType() != cel.BoolType && ast.OutputType() != cel.DynType {
			return nil, fmt.Errorf("expression must return bool")
		}
		return env.Program(ast, cel.CostLimit(10000), cel.InterruptCheckFrequency(100))
	}
	result := make([]CompiledRule, 0, len(c.Rules))
	for _, spec := range c.Rules {
		a, err := compile(spec.Trigger)
		if err != nil {
			return nil, fmt.Errorf("rule %s trigger: %w", spec.ID, err)
		}
		b, err := compile(spec.Recover)
		if err != nil {
			return nil, fmt.Errorf("rule %s recover: %w", spec.ID, err)
		}
		result = append(result, CompiledRule{Spec: spec, trigger: a, recover: b})
	}
	return result, nil
}

// Evaluate deliberately requires an explicit recovery expression. A trigger
// evaluating false, a missing row, or a CEL error never means recovered.
func (r CompiledRule) Evaluate(o Observation, previous map[string]any, now time.Time) RuleResult {
	result := RuleResult{RuleID: r.Spec.ID, Version: r.Spec.Version, Decision: Unknown}
	if r.trigger == nil || r.recover == nil {
		result.Reason = "rule_not_compiled"
		return result
	}
	if o.Quality != QualityValid {
		result.Reason = "invalid_observation"
		return result
	}
	if previous == nil {
		previous = map[string]any{}
	}
	activation := map[string]any{"fields": o.Fields, "previous": previous, "now": now.UTC(), "entity_key": o.EntityKey}
	eval := func(p cel.Program) (bool, error) {
		v, _, err := p.Eval(activation)
		if err != nil || types.IsError(v) || types.IsUnknown(v) {
			return false, fmt.Errorf("expression_evaluation_failed")
		}
		b, ok := v.(types.Bool)
		if !ok {
			return false, fmt.Errorf("expression_result_not_bool")
		}
		return bool(b), nil
	}
	bad, err := eval(r.trigger)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	good, err := eval(r.recover)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	if bad && good {
		result.Reason = "conflicting_trigger_and_recovery"
		return result
	}
	if bad {
		result.Decision = Trigger
	} else if good {
		result.Decision = Clear
	} else {
		result.Reason = "neither_trigger_nor_recovery"
	}
	return result
}
