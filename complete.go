package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Tab completion. The shell scripts below call `vault __complete c:<current word> <earlier words...>`
// and show what it prints, one candidate per line. Entry names only come from an
// unlocked session (`vault unlock` / the GUI): completion never prompts for the
// password and never writes names anywhere unencrypted.

var commandNames = []string{
	"add", "completion", "edit", "export", "find", "get", "gui", "help", "import", "init",
	"lock", "ls", "mv", "passwd", "remote", "rm", "set", "status", "sync", "unlock", "version",
}

var flagNames = []string{"--print", "--show", "--ttl", "--timeout", "--secret", "--yes", "--help", "--version"}

// commands that take an entry/group name
var keyCommands = map[string]bool{
	"get": true, "ls": true, "list": true, "edit": true, "set": true, "rm": true, "del": true,
	"delete": true, "mv": true, "rename": true, "export": true, "add": true, "new": true,
}

// commands whose arguments we never complete
var noArgCommands = map[string]bool{
	"init": true, "remote": true, "status": true, "gui": true, "unlock": true, "lock": true,
	"passwd": true, "help": true, "version": true, "sync": true, "import": true, "find": true, "search": true,
}

func runComplete(args []string) {
	cur := ""
	if len(args) > 0 && strings.HasPrefix(args[0], "c:") {
		cur = args[0][2:]
		args = args[1:]
	}
	// earlier positional words, skipping flags and their values
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--ttl" || a == "--timeout" {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		pos = append(pos, a)
	}
	for _, c := range completions(pos, cur) {
		fmt.Println(c)
	}
}

func completions(pos []string, cur string) []string {
	if strings.HasPrefix(cur, "-") {
		return filterPrefix(flagNames, cur)
	}
	var out []string
	if len(pos) == 0 {
		out = append(out, filterPrefix(commandNames, cur)...)
	} else {
		cmd := pos[0]
		if cmd == "completion" {
			if len(pos) == 1 {
				return filterPrefix([]string{"bash", "zsh", "fish", "powershell"}, cur)
			}
			return nil
		}
		if noArgCommands[cmd] {
			return nil
		}
		if !keyCommands[cmd] && len(pos) > 0 {
			// `vault prod db <TAB>`: a fuzzy multi-word lookup — nothing sensible to offer
			return nil
		}
	}
	ag := findAgent()
	if ag == nil {
		return out
	}
	if st, err := ag.status(); err != nil || !st.Unlocked {
		return out
	}
	v, err := ag.Snapshot()
	if err != nil {
		return out
	}
	return append(out, keyCompletions(v, cur)...)
}

// keyCompletions offers groups ("globex/"), entries ("globex/api-server") and,
// once an entry is typed with a trailing separator, its fields
// ("globex/api-server/password"). Typing with dots keeps dots.
func keyCompletions(v *Vault, cur string) []string {
	dot := strings.Contains(cur, ".") && !strings.Contains(cur, "/")
	q := cur
	if dot {
		q = strings.ReplaceAll(cur, ".", "/")
	}
	// show one level at a time, like file paths: "wis" -> "globex/",
	// "globex/" -> its entries, "globex/api-server/" -> its fields
	dir := ""
	if i := strings.LastIndex(q, "/"); i >= 0 {
		dir = strings.ToLower(q[:i+1])
	}
	set := map[string]bool{}
	for _, p := range v.Paths() {
		lp := strings.ToLower(p)
		if lp+"/" == dir {
			for _, f := range v.Entries[p].Fields {
				set[p+"/"+f.Key] = true
			}
		}
		if !strings.HasPrefix(lp, dir) {
			continue
		}
		rest := p[len(dir):]
		if j := strings.Index(rest, "/"); j >= 0 {
			set[p[:len(dir)+j+1]] = true
		} else {
			set[p] = true
		}
	}
	var all []string
	for c := range set {
		all = append(all, c)
	}
	sort.Strings(all)
	m := filterPrefix(all, q)
	if dot {
		for i := range m {
			m[i] = strings.ReplaceAll(m[i], "/", ".")
		}
	}
	return m
}

func filterPrefix(list []string, prefix string) []string {
	lp := strings.ToLower(prefix)
	var out []string
	for _, s := range list {
		if strings.HasPrefix(strings.ToLower(s), lp) {
			out = append(out, s)
		}
	}
	return out
}

const bashCompletion = `# vault tab completion for bash — add to ~/.bashrc:
#   eval "$(vault completion bash)"
_vault_complete() {
  local IFS=$'\n'
  local cur="${COMP_WORDS[COMP_CWORD]}"
  COMPREPLY=($(vault __complete "c:$cur" "${COMP_WORDS[@]:1:COMP_CWORD-1}" 2>/dev/null))
  if [[ ${#COMPREPLY[@]} -eq 1 && ( ${COMPREPLY[0]} == */ || ${COMPREPLY[0]} == *. ) ]]; then
    compopt -o nospace 2>/dev/null
  fi
}
complete -o default -F _vault_complete vault
`

const zshCompletion = `# vault tab completion for zsh — add to ~/.zshrc:
#   eval "$(vault completion zsh)"
(( $+functions[compdef] )) || { autoload -Uz compinit && compinit -i }
_vault_complete() {
  local -a all groups items
  all=("${(@f)$(vault __complete "c:${words[CURRENT]}" "${(@)words[2,CURRENT-1]}" 2>/dev/null)}")
  local x
  for x in $all; do
    [[ -z $x ]] && continue
    if [[ $x == */ || $x == *. ]]; then groups+=("$x"); else items+=("$x"); fi
  done
  (( ${#groups} )) && compadd -S '' -- $groups
  (( ${#items} )) && compadd -- $items
  (( ${#groups} + ${#items} )) || _files
}
compdef _vault_complete vault
`

const fishCompletion = `# vault tab completion for fish — save it once:
#   vault completion fish > ~/.config/fish/completions/vault.fish
function __vault_complete
    set -l cur (commandline -ct)
    set -l words (commandline -opc)
    vault __complete "c:$cur" $words[2..-1] 2>/dev/null
end
complete -c vault -f -a '(__vault_complete)'
complete -c vault -n '__fish_seen_subcommand_from import' -F
`

const powershellCompletion = `# vault tab completion for PowerShell — add to your profile (notepad $PROFILE):
#   vault completion powershell | Out-String | Invoke-Expression
Register-ArgumentCompleter -Native -CommandName vault, vault.exe -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)
    $words = @($commandAst.CommandElements | Select-Object -Skip 1 | ForEach-Object { $_.ToString() })
    if ($wordToComplete -ne '' -and $words.Count -gt 0) { $words = @($words | Select-Object -SkipLast 1) }
    & vault.exe __complete "c:$wordToComplete" @words 2>$null | ForEach-Object {
        [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
    }
}
`

func cmdCompletion(a []string) error {
	if len(a) != 1 {
		return errors.New("usage: vault completion bash|zsh|fish|powershell")
	}
	switch strings.ToLower(a[0]) {
	case "bash":
		fmt.Print(bashCompletion)
	case "zsh":
		fmt.Print(zshCompletion)
	case "fish":
		fmt.Print(fishCompletion)
	case "powershell", "pwsh":
		fmt.Print(powershellCompletion)
	default:
		return fmt.Errorf("unknown shell %q — use bash, zsh, fish or powershell", a[0])
	}
	return nil
}
