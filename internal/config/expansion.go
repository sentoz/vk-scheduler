package config

import (
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	innerExpansion   = regexp.MustCompile(`\$\{([^{}]+)\}`)
	escapedExpansion = regexp.MustCompile(`\$\$\{([^{}]+)\}`)
	expansionName    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func checkExpansionSafety(root *yaml.Node, lookup func(string) (string, bool)) error {
	if root.Kind == yaml.AliasNode {
		return ErrInvalidConfig
	}
	if root.Kind == yaml.ScalarNode {
		if err := checkScalarExpansion(root.Value, lookup); err != nil {
			return err
		}
	}
	for _, child := range root.Content {
		if err := checkExpansionSafety(child, lookup); err != nil {
			return err
		}
	}
	return nil
}

func checkScalarExpansion(input string, lookup func(string) (string, bool)) error {
	value := escapedExpansion.ReplaceAllString(input, "\x00$1\x01")
	value = escapedExpansion.ReplaceAllString(value, "\x00$1\x01")
	for range 10 {
		if !innerExpansion.MatchString(value) {
			if strings.Contains(value, "${") {
				return ErrInvalidConfig
			}
			return nil
		}
		var failure error
		next := innerExpansion.ReplaceAllStringFunc(value, func(match string) string {
			if failure != nil {
				return match
			}
			name, tail, hasTail := strings.Cut(match[2:len(match)-1], ":")
			if !expansionName.MatchString(name) {
				failure = ErrInvalidConfig
				return match
			}
			if hasTail && strings.HasPrefix(tail, "=") {
				failure = ErrAssigningExpansion
				return match
			}
			found, exists := lookup(name)
			if !hasTail {
				return found
			}
			if tail == "" {
				return found
			}
			if exists && found != "" {
				return found
			}
			switch tail[0] {
			case '?':
				failure = ErrInvalidConfig
				return match
			case '-':
				return tail[1:]
			default:
				return tail
			}
		})
		if failure != nil {
			return failure
		}
		if next == value {
			return ErrInvalidConfig
		}
		value = next
	}
	if innerExpansion.MatchString(value) {
		return ErrInvalidConfig
	}
	return nil
}
