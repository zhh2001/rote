//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhh2001/rote/internal/config"
)

func TestDefaultConfigProcess(t *testing.T) {
	for _, useXDG := range []bool{true, false} {
		name := "xdg_empty"
		if useXDG {
			name = "xdg_set"
		}
		t.Run(name, func(t *testing.T) {
			cfgDir := isolateConfigDir(t, useXDG)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			path := filepath.Join(cfgDir, "rote", "jobs.toml")

			p := newCLIProcess(t, "init")
			p.start(t)
			p.wait(t, 0)
			if !strings.Contains(p.output.String(), path) {
				t.Fatalf("init did not report the native config path %q: %s", path, p.output.String())
			}
			if jobs, err := config.Load(path); err != nil || len(jobs) != 1 || jobs[0].Name != "example" {
				t.Fatalf("default init generated invalid config: jobs=%+v err=%v", jobs, err)
			}

			// A user edit must survive another init, and readers must use the
			// same default path as init without an explicit -c flag.
			original := "# preserved user configuration\n[[job]]\nname='native-path'\nschedule='@hourly'\ncommand='echo native-config-output'\n"
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				args []string
				want string
			}{
				{[]string{"init"}, "not overwriting"},
				{[]string{"list"}, "native-path"},
				{[]string{"run", "native-path"}, "native-config-output"},
				{[]string{"logs", "-o", "native-path"}, "native-config-output"},
			} {
				p = newCLIProcess(t, tc.args...)
				p.start(t)
				p.wait(t, 0)
				if !strings.Contains(p.output.String(), tc.want) {
					t.Fatalf("%v did not print %q: %s", tc.args, tc.want, p.output.String())
				}
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != original {
				t.Fatalf("default config was changed: data=%q err=%v", data, err)
			}
		})
	}
}
