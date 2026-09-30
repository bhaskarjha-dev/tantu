package main

import (
	"reflect"
	"testing"
)

// NormalizeArgs hoists flags — including a flag's separate value — ahead of
// every positional. That is the right shape for a FlagSet, and the wrong shape
// for a subcommand dispatcher, which is exactly what runTransfer was: it read
// args[0] and so rejected its own usage text.
//
//	$ tantu transfer list --json
//	Unknown transfer subcommand: --json
//
// Every documented form carrying a flag failed, while the flagless form
// worked, so the command looked broken rather than mistyped.
func TestTransferDispatchFindsTheSubcommandAfterHoistedFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
		// wantArgs is what the sub-runner must receive, flags included.
		wantArgs []string
	}{
		{name: "bare defaults to list", args: []string{}, want: "", wantArgs: []string{}},
		{name: "explicit list", args: []string{"list"}, want: "list", wantArgs: []string{}},
		{name: "documented list --json", args: []string{"list", "--json"}, want: "list", wantArgs: []string{"--json"}},
		{name: "flag before subcommand", args: []string{"--json", "list"}, want: "list", wantArgs: []string{"--json"}},
		{
			name: "documented clear --yes --store-dir",
			args: []string{"clear", "--yes", "--store-dir", `C:\tmp\store`},
			want: "clear",
			// --store-dir consumes its value, so the directory must not be
			// mistaken for the subcommand either.
			wantArgs: []string{"--yes", "--store-dir", `C:\tmp\store`},
		},
		{
			name:     "store-dir value that shares a subcommand name",
			args:     []string{"list", "--store-dir", "clear"},
			want:     "list",
			wantArgs: []string{"--store-dir", "clear"},
		},
		{
			name:     "flagged positional after the subcommand",
			args:     []string{"list", "--json", "extra"},
			want:     "list",
			wantArgs: []string{"--json", "extra"},
		},
		{name: "flag only still defaults to list", args: []string{"--json"}, want: "", wantArgs: []string{"--json"}},
		{name: "retry is refused by name", args: []string{"retry"}, want: "retry", wantArgs: []string{}},
		{name: "unknown subcommand is reported by name", args: []string{"bogus"}, want: "bogus", wantArgs: []string{}},
		{
			name:     "unknown subcommand keeps its flags",
			args:     []string{"bogus", "--json"},
			want:     "bogus",
			wantArgs: []string{"--json"},
		},
		{name: "inline flag assignment", args: []string{"list", "--json=true"}, want: "list", wantArgs: []string{"--json=true"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gotArgs := transferDispatch(tc.args)
			if got != tc.want {
				t.Errorf("subcommand = %q, want %q", got, tc.want)
			}
			if !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Errorf("sub args = %#v, want %#v", gotArgs, tc.wantArgs)
			}
		})
	}
}

// The whole scheme rests on knowing which flags take a separate value. If a
// value-taking flag were missing from valueFlags, its value would be read as
// the subcommand and the command would dispatch to the wrong place — a
// misparse that looks like a user error rather than a bug. TestSendValueFlagMapIsComplete
// keeps the map honest against the flag sets; this checks the consumer.
func TestSplitLeadingFlagsSkipsValuesWithoutPanicking(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantFlags    []string
		wantPosition []string
	}{
		{
			name:         "value flag consumes its value",
			args:         []string{"--store-dir", "/tmp/s", "clear"},
			wantFlags:    []string{"--store-dir", "/tmp/s"},
			wantPosition: []string{"clear"},
		},
		{
			name:         "boolean flag consumes nothing",
			args:         []string{"--yes", "clear"},
			wantFlags:    []string{"--yes"},
			wantPosition: []string{"clear"},
		},
		{
			name:         "inline assignment is one token",
			args:         []string{"--store-dir=/tmp/s", "clear"},
			wantFlags:    []string{"--store-dir=/tmp/s"},
			wantPosition: []string{"clear"},
		},
		{
			name:         "no flags at all",
			args:         []string{"list"},
			wantFlags:    nil,
			wantPosition: []string{"list"},
		},
		{
			name:         "empty input",
			args:         nil,
			wantFlags:    nil,
			wantPosition: nil,
		},
		{
			name:         "stops at a double dash",
			args:         []string{"--json", "--", "clear"},
			wantFlags:    []string{"--json"},
			wantPosition: []string{"--", "clear"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			flags, pos := splitLeadingFlags(tc.args)
			if !reflect.DeepEqual(flags, tc.wantFlags) {
				t.Errorf("flags = %#v, want %#v", flags, tc.wantFlags)
			}
			if !reflect.DeepEqual(pos, tc.wantPosition) {
				t.Errorf("positionals = %#v, want %#v", pos, tc.wantPosition)
			}
		})
	}
}
