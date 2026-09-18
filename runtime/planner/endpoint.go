package planner

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/builtwithtofu/sigil/core/decorator"
	"github.com/builtwithtofu/sigil/core/planfmt"
	"github.com/builtwithtofu/sigil/runtime/lexer"
)

var endpointDisplayID = regexp.MustCompile(`sigil:[A-Za-z0-9_-]{22}`)

func (e *Emitter) recordEndpointUses(value planfmt.Value, key string) {
	switch value.Kind {
	case planfmt.ValueString:
		seen := map[string]bool{}
		for _, id := range endpointDisplayID.FindAllString(value.Str, -1) {
			if !seen[id] {
				e.recordSecretUse("", id, key)
				seen[id] = true
			}
		}
	case planfmt.ValueArray:
		for _, item := range value.Array {
			e.recordEndpointUses(item, key)
		}
	case planfmt.ValueMap:
		keys := make([]string, 0, len(value.Map))
		for name := range value.Map {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, name := range keys {
			e.recordEndpointUses(value.Map[name], key)
		}
	}
}

// A bare endpoint is one path value, not a command for a selected shell.
func endpointPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 && (raw[0] == '"' || raw[0] == '\'') {
		lex := lexer.NewLexer()
		lex.Init([]byte(raw))
		token := lex.NextToken()
		if token.Type != lexer.STRING || lex.NextToken().Type != lexer.EOF {
			return "", fmt.Errorf("redirect requires one quoted path value")
		}
		// Use the same literal conversion as explicit decorator arguments.
		return tokenToValue(token).(string), nil
	}
	if raw == "" || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("redirect requires one path value; quote paths containing spaces")
	}
	return raw, nil
}

func (e *Emitter) normalizeEndpoint(target *planfmt.CommandNode, mode string) error {
	endpoint, ok, reason := decorator.Global().GetRedirectTarget(strings.TrimPrefix(target.Decorator, "@"))
	if !ok {
		return fmt.Errorf("invalid endpoint %s: %s", target.Decorator, reason)
	}
	caps := endpoint.IOCaps()
	if (mode == ">" && !caps.Write) || (mode == ">>" && !caps.Append) || (mode == "<" && !caps.Read) {
		return fmt.Errorf("endpoint %s does not support %s", target.Decorator, mode)
	}
	schema := endpoint.Descriptor().Schema
	raw := make(map[string]any, len(target.Args))
	for _, arg := range target.Args {
		raw[arg.Key] = arg.Val
	}
	normalized, _, err := decorator.NormalizeArgs(schema, nil, raw)
	if err != nil {
		return fmt.Errorf("endpoint %s: %w", target.Decorator, err)
	}
	for key, param := range schema.Parameters {
		if _, ok := normalized[key]; ok {
			continue
		}
		if param.Default != nil {
			value, err := e.literalToArgValue(param.Default, nil)
			if err != nil {
				return err
			}
			normalized[key] = value
		} else if param.Required {
			return fmt.Errorf("endpoint %s requires %s", target.Decorator, key)
		}
	}
	if target.Decorator == "@file" {
		if atomic, ok := normalized["atomic"].(planfmt.Value); ok && atomic.Kind == planfmt.ValueBool && atomic.Bool && mode != ">" {
			return fmt.Errorf("atomic publication requires replacement (>), not %s", mode)
		}
		if perm, ok := normalized["perm"].(planfmt.Value); ok && perm.Kind == planfmt.ValueInt && (perm.Int < 0 || perm.Int > 0o777) {
			return fmt.Errorf("file permissions must be between 0 and 0777")
		}
	}
	target.Args = nil
	for key, value := range normalized {
		target.Args = append(target.Args, planfmt.Arg{Key: key, Val: value.(planfmt.Value)})
	}
	sort.Slice(target.Args, func(i, j int) bool { return target.Args[i].Key < target.Args[j].Key })
	return nil
}
