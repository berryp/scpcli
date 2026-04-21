package commands

import (
	"fmt"
	"strings"

	"github.com/alecthomas/kong"
)

// globalFlags are pre-parsed before Kong sees args, so they aren't in the
// model. List them manually so completion still surfaces them.
var globalFlags = []struct{ name, desc string }{
	{"version", "Print version information and quit"},
	{"output-file", "Write response to a file instead of stdout"},
	{"filter", "yq expression to filter output"},
	{"print", "Print request code instead of calling API (curl, python)"},
	{"completion", "Generate shell completion script (fish, bash)"},
}

func generateCompletion(k *kong.Kong, shell string) error {
	switch shell {
	case "fish":
		return completionFish(k)
	case "bash":
		return completionBash(k)
	default:
		return fmt.Errorf("unknown shell %q (supported: fish, bash)", shell)
	}
}

func completionFish(k *kong.Kong) error {
	var sb strings.Builder
	appName := k.Model.Name

	// Disable file completion globally.
	fmt.Fprintf(&sb, "complete -c %s -f\n\n", appName)

	// Global flags (pre-parsed, not in the Kong model).
	for _, gf := range globalFlags {
		fmt.Fprintf(&sb, "complete -c %s -l %s -d %q\n", appName, gf.name, gf.desc)
	}
	sb.WriteString("\n")

	// Collect all service names for the "no service selected yet" condition.
	services := k.Model.Children
	svcNames := make([]string, len(services))
	for i, s := range services {
		svcNames[i] = s.Name
	}
	noSvc := fmt.Sprintf("not __fish_seen_subcommand_from %s", strings.Join(svcNames, " "))

	// Level 1: service completions.
	for _, svc := range services {
		fmt.Fprintf(&sb, "complete -c %s -n %q -a %q -d %q\n",
			appName, noSvc, svc.Name, svc.Help)
	}
	sb.WriteString("\n")

	// Level 2 & 3: per-service operation and flag completions.
	for _, svc := range services {
		ops := svc.Children
		opNames := make([]string, len(ops))
		for i, op := range ops {
			opNames[i] = op.Name
		}
		seenSvc := fmt.Sprintf("__fish_seen_subcommand_from %s", svc.Name)
		noOp := fmt.Sprintf("not __fish_seen_subcommand_from %s", strings.Join(opNames, " "))

		// Operations.
		for _, op := range ops {
			fmt.Fprintf(&sb, "complete -c %s -n %q -a %q -d %q\n",
				appName,
				seenSvc+"; and "+noOp,
				op.Name, op.Help)
		}

		// Flags per operation.
		for _, op := range ops {
			seenOp := fmt.Sprintf("__fish_seen_subcommand_from %s", op.Name)
			cond := seenSvc + "; and " + seenOp
			for _, flag := range op.Flags {
				if flag.Hidden {
					continue
				}
				fmt.Fprintf(&sb, "complete -c %s -n %q -l %s -d %q\n",
					appName, cond, flag.Name, flag.Help)
			}
		}
		sb.WriteString("\n")
	}

	fmt.Print(sb.String())
	return nil
}

func completionBash(k *kong.Kong) error {
	appName := k.Model.Name
	var sb strings.Builder

	fmt.Fprintf(&sb, "_%s() {\n", appName)
	sb.WriteString("  local cur prev words cword\n")
	sb.WriteString("  _init_completion || return\n\n")

	// Build service→operations map inline.
	sb.WriteString("  local services=\"")
	svcNames := make([]string, len(k.Model.Children))
	for i, s := range k.Model.Children {
		svcNames[i] = s.Name
	}
	sb.WriteString(strings.Join(svcNames, " "))
	sb.WriteString("\"\n\n")

	sb.WriteString("  # Depth 1: complete service names\n")
	sb.WriteString("  if [[ $cword -eq 1 ]]; then\n")
	sb.WriteString("    COMPREPLY=($(compgen -W \"$services\" -- \"$cur\"))\n")
	sb.WriteString("    return\n")
	sb.WriteString("  fi\n\n")

	sb.WriteString("  # Depth 2: complete operations for the selected service\n")
	sb.WriteString("  case \"${words[1]}\" in\n")
	for _, svc := range k.Model.Children {
		opNames := make([]string, len(svc.Children))
		for i, op := range svc.Children {
			opNames[i] = op.Name
		}
		fmt.Fprintf(&sb, "    %s)\n", svc.Name)
		fmt.Fprintf(&sb, "      if [[ $cword -eq 2 ]]; then\n")
		fmt.Fprintf(&sb, "        COMPREPLY=($(compgen -W %q -- \"$cur\"))\n", strings.Join(opNames, " "))
		sb.WriteString("        return\n")
		sb.WriteString("      fi\n")

		// Depth 3: flags per operation.
		sb.WriteString("      case \"${words[2]}\" in\n")
		for _, op := range svc.Children {
			flagNames := make([]string, 0, len(op.Flags))
			for _, f := range op.Flags {
				if !f.Hidden {
					flagNames = append(flagNames, "--"+f.Name)
				}
			}
			fmt.Fprintf(&sb, "        %s)\n", op.Name)
			fmt.Fprintf(&sb, "          COMPREPLY=($(compgen -W %q -- \"$cur\"))\n", strings.Join(flagNames, " "))
			sb.WriteString("          return ;;\n")
		}
		sb.WriteString("      esac ;;\n")
	}
	sb.WriteString("  esac\n")
	fmt.Fprintf(&sb, "}\ncomplete -F _%s %s\n", appName, appName)

	fmt.Print(sb.String())
	return nil
}
