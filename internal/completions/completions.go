// Package completions generates and installs tab-completion scripts for zsh and bash.
package completions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const ZshScript = `#compdef agy-rotator

_agy_rotator_emails() {
    local -a emails
    if command -v agy-rotator >/dev/null 2>&1; then
        emails=($(agy-rotator list 2>/dev/null | awk '{print $2}' | grep -v '^$' | grep '@'))
    fi
    _describe 'accounts' emails
}

_agy_rotator() {
    local -a commands
    commands=(
        'add:Capture account(s) via browser sign-in links'
        'list:List vaulted accounts'
        'status:Show active account and cooldowns'
        'quota:Preview live remaining quota per model'
        'stats:Show local CLI conversation counts, steps and usage metrics'
        'why:Explain why current account is active and selection state'
        'history:Audit log of rotations, cooldowns, and failures'
        'statusline:Emit compact status-bar segment for terminal statuslines'
        'proxy:Manage in-flight transparent request proxy'
        'rotate:Switch live credentials to next account now'
        'use:Activate a specific account'
        'remove:Forget an account'
        'doctor:Re-validate stored refresh tokens against Google'
        'export:Export passphrase-encrypted vault envelope'
        'import:Import accounts from encrypted vault envelope'
        'config:View or update configuration settings'
        'watch:Auto-cooldown and rotate on quota errors'
        'set-mode:Set selection mode (round-robin | sticky | smart)'
        'plugin:Manage agy-native plugin skill bundle'
        'shim:Manage PATH launch shim'
        'completions:Output or install shell completions'
        'version:Print version'
        'help:Show help'
    )

    _arguments -C \
        '1: :->command' \
        '*:: :->args'

    case $state in
        command)
            _describe -t commands 'agy-rotator command' commands
            ;;
        args)
            case $words[1] in
                use|remove)
                    _arguments \
                        '-email[Account email to target]:email:_agy_rotator_emails'
                    ;;
                quota)
                    _arguments \
                        '-email[Target specific account]:email:_agy_rotator_emails' \
                        '-json[Output JSON]'
                    ;;
                doctor)
                    _arguments \
                        '-email[Target specific account]:email:_agy_rotator_emails' \
                        '-fix[Prune revoked accounts automatically]'
                    ;;
                stats)
                    _arguments \
                        '-json[Output JSON]'
                    ;;
                export)
                    _arguments \
                        '-passphrase[Passphrase to encrypt envelope]:passphrase:' \
                        '-out[Output file path]:file:_files'
                    ;;
                import)
                    _arguments \
                        '-passphrase[Passphrase to decrypt envelope]:passphrase:' \
                        '-replace[Replace all existing accounts]' \
                        '1:file:_files'
                    ;;
                config)
                    _values 'action' 'get' 'set' 'list'
                    ;;
                history)
                    _arguments \
                        '-n[Max events to show]:limit:' \
                        '-email[Filter by email]:email:_agy_rotator_emails' \
                        '-json[Output JSON]'
                    ;;
                statusline)
                    _arguments \
                        '--no-color[Disable ANSI color escapes]' \
                        '--json[Output JSON]'
                    ;;
                proxy)
                    _values 'action' 'start' 'stop' 'status' 'daemon' 'cert'
                    ;;
                watch)
                    _values 'action' 'install-service' 'uninstall-service' 'status-service'
                    ;;
                set-mode)
                    _values 'mode' 'round-robin' 'sticky' 'smart'
                    ;;
                plugin)
                    _values 'action' 'install' 'uninstall' 'status'
                    ;;
                shim)
                    _values 'action' 'install' 'uninstall' 'print'
                    ;;
                completions)
                    _values 'action' 'zsh' 'bash' 'install'
                    ;;
            esac
            ;;
    esac
}

_agy_rotator "$@"
`

const BashScript = `_agy_rotator_completions() {
    local cur prev words cword
    _init_completion || return

    local commands="add list status quota stats why history statusline proxy rotate use remove doctor export import config watch set-mode plugin shim completions version help"

    if [[ $cword -eq 1 ]]; then
        COMPREPLY=($(compgen -W "$commands" -- "$cur"))
        return 0
    fi

    case "${words[1]}" in
        use|remove)
            if [[ "$prev" == "-email" ]]; then
                local emails=$(agy-rotator list 2>/dev/null | awk '{print $2}' | grep '@')
                COMPREPLY=($(compgen -W "$emails" -- "$cur"))
            else
                COMPREPLY=($(compgen -W "-email" -- "$cur"))
            fi
            ;;
        quota)
            if [[ "$prev" == "-email" ]]; then
                local emails=$(agy-rotator list 2>/dev/null | awk '{print $2}' | grep '@')
                COMPREPLY=($(compgen -W "$emails" -- "$cur"))
            else
                COMPREPLY=($(compgen -W "-email -json" -- "$cur"))
            fi
            ;;
        doctor)
            if [[ "$prev" == "-email" ]]; then
                local emails=$(agy-rotator list 2>/dev/null | awk '{print $2}' | grep '@')
                COMPREPLY=($(compgen -W "$emails" -- "$cur"))
            else
                COMPREPLY=($(compgen -W "-email -fix" -- "$cur"))
            fi
            ;;
        config)
            COMPREPLY=($(compgen -W "get set list" -- "$cur"))
            ;;
        proxy)
            COMPREPLY=($(compgen -W "start stop status daemon cert" -- "$cur"))
            ;;
        watch)
            COMPREPLY=($(compgen -W "install-service uninstall-service status-service" -- "$cur"))
            ;;
        set-mode)
            COMPREPLY=($(compgen -W "round-robin sticky smart" -- "$cur"))
            ;;
        plugin)
            COMPREPLY=($(compgen -W "install uninstall status" -- "$cur"))
            ;;
        shim)
            COMPREPLY=($(compgen -W "install uninstall print" -- "$cur"))
            ;;
        completions)
            COMPREPLY=($(compgen -W "zsh bash install" -- "$cur"))
            ;;
    esac
}

complete -F _agy_rotator_completions agy-rotator
`

// Install writes completion files to standard user directories and returns the installed path.
func Install(shell string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	if shell == "" {
		s := os.Getenv("SHELL")
		if strings.Contains(s, "zsh") {
			shell = "zsh"
		} else {
			shell = "bash"
		}
	}

	switch shell {
	case "zsh":
		dir := filepath.Join(home, ".local", "share", "zsh", "site-functions")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		p := filepath.Join(dir, "_agy-rotator")
		if err := os.WriteFile(p, []byte(ZshScript), 0o644); err != nil {
			return "", err
		}
		return p, nil
	case "bash":
		dir := filepath.Join(home, ".local", "share", "bash-completion", "completions")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		p := filepath.Join(dir, "agy-rotator")
		if err := os.WriteFile(p, []byte(BashScript), 0o644); err != nil {
			return "", err
		}
		return p, nil
	default:
		return "", fmt.Errorf("unsupported shell %q (supported: zsh, bash)", shell)
	}
}
