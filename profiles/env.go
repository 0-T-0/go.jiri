// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package profiles

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"v.io/jiri/project"
	"v.io/jiri/tool"
	"v.io/jiri/util"
	"v.io/x/lib/envvar"
)

// GoFlags lists all of the Go environment variables and will be sorted in the
// init function for this package.
var GoFlags = []string{
	"CC",
	"CC_FOR_TARGET",
	"CGO_ENABLED",
	"CXX_FOR_TARGET",
	"GO15VENDOREXPERIMENT",
	"GOARCH",
	"GOBIN",
	"GOEXE",
	"GOGCCFLAGS",
	"GOHOSTARCH",
	"GOHOSTOS",
	"GOOS",
	"GOPATH",
	"GORACE",
	"GOROOT",
	"GOTOOLDIR",
}

func init() {
	sort.Strings(GoFlags)
}

// UnsetGoEnv unsets Go environment variables in the given environment.
func UnsetGoEnv(env *envvar.Vars) {
	for _, k := range GoFlags {
		env.Set(k, "")
	}
}

// GoEnvironmentFromOS() returns the values of all Go environment variables
// as set via the OS; unset variables are omitted.
func GoEnvironmentFromOS() []string {
	os := envvar.SliceToMap(os.Environ())
	vars := make([]string, 0, len(GoFlags))
	for _, k := range GoFlags {
		v, present := os[k]
		if !present {
			continue
		}
		vars = append(vars, envvar.JoinKeyValue(k, v))
	}
	return vars
}

// ConfigHelper wraps the various sources of configuration and profile
// information to provide convenient methods for determing the environment
// variables to use for a given situation. It creates an initial copy of the OS
// environment that is mutated by its various methods.
type ConfigHelper struct {
	*envvar.Vars
	legacyMode bool
	root       string
	ctx        *tool.Context
	config     *util.Config
	projects   project.Projects
	tools      project.Tools
}

// NewConfigHelper creates a new config helper. If filename is of non-zero
// length then that file will be read as a profiles manifest file, if not, the
// existing, if any, in-memory profiles information will be used.
func NewConfigHelper(ctx *tool.Context, filename string) (*ConfigHelper, error) {
	root, err := project.JiriRoot()
	if err != nil {
		return nil, err
	}
	config, err := util.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	projects, tools, err := project.ReadManifest(ctx)
	if err != nil {
		return nil, err
	}
	if len(filename) > 0 {
		if err := Read(ctx, filepath.Join(root, filename)); err != nil {
			return nil, err
		}
	}
	ch := &ConfigHelper{
		ctx:      ctx,
		root:     root,
		config:   config,
		projects: projects,
		tools:    tools,
	}
	ch.legacyMode = (SchemaVersion() == Original) || (len(os.Getenv("JIRI_PROFILE")) > 0)
	if ch.legacyMode {
		vars, err := util.JiriLegacyEnvironment(ch.ctx)
		if err != nil {
			return nil, err
		}
		ch.Vars = vars
	} else {
		ch.Vars = envvar.VarsFromOS()
	}
	return ch, nil
}

// Root returns the root of the jiri universe.
func (ch *ConfigHelper) Root() string {
	return ch.root
}

// LegacyProfiles returns true if the old-style profiles are being used.
func (ch *ConfigHelper) LegacyProfiles() bool {
	return ch.legacyMode
}

// CommonConcatVariables returns a map of variables that are commonly
// used for the concat parameter to SetEnvFromProfilesAndTarget.
func CommonConcatVariables() map[string]string {
	return map[string]string{
		"PATH":         ":",
		"CCFLAGS":      " ",
		"CXXFLAGS":     " ",
		"LDFLAGS":      " ",
		"CGO_CFLAGS":   " ",
		"CGO_CXXFLAGS": " ",
		"CGO_LDFLAGS":  " ",
	}
}

// CommonIgnoreVariables returns a map of variables that are commonly
// used for the ignore parameter to SetEnvFromProfilesAndTarget.
func CommonIgnoreVariables() map[string]bool {
	return map[string]bool{
		"GOPATH": true,
	}
}

// SetEnvFromProfiles populates the embedded environment with the environment
// variables stored in the specified profiles for the specified target if
// new-style profiles are being used, otherwise it uses compiled in values as per
// the original profiles implementation.
// The profiles parameter contains a comma separated list of profile names; if the
// requested target does not exist for any of these profiles then those profiles
// will be ignored. The 'concat' parameter includes a map of variable names
// whose values are to concatenated with any existing ones rather than
// overwriting them (e.g. CFLAGS for example). The value of the concat map
// is the separator to use for that environment  variable (e.g. space for
// CFLAGs or ':' for PATH-like ones).
func (ch *ConfigHelper) SetEnvFromProfiles(concat map[string]string, ignore map[string]bool, profiles string, target Target) {
	if ch.legacyMode {
		return
	}
	for _, profile := range strings.Split(profiles, ",") {
		t := LookupProfileTarget(profile, target)
		if t == nil {
			continue
		}
		for _, tmp := range t.Env.Vars {
			k, v := envvar.SplitKeyValue(tmp)
			if ignore[k] {
				continue
			}
			if sep := concat[k]; len(sep) > 0 {
				ov := ch.Vars.GetTokens(k, sep)
				nv := envvar.SplitTokens(v, sep)
				ch.Vars.SetTokens(k, append(ov, nv...), " ")
				continue
			}
			ch.Vars.Set(k, v)
		}
	}
}

// MergeEnv merges vars with the variables in env taking care to concatenate
// values as per the concat and ignore parameters similarly to SetEnvFromProfiles.
func MergeEnv(concat map[string]string, ignore map[string]bool, env *envvar.Vars, vars ...[]string) {
	for _, ev := range vars {
		for _, tmp := range ev {
			k, v := envvar.SplitKeyValue(tmp)
			if ignore[k] {
				continue
			}
			if sep := concat[k]; len(sep) > 0 {
				ov := env.GetTokens(k, sep)
				nv := envvar.SplitTokens(v, sep)
				env.SetTokens(k, append(ov, nv...), " ")
				continue
			}
			env.Set(k, v)
		}
	}
}

// MergeEnvFromProfiles merges the environment variables stored in the specified
// profiles and target with the env parameter. It uses MergeEnv to do so.
func MergeEnvFromProfiles(concat map[string]string, ignore map[string]bool, env *envvar.Vars, target Target, profileNames ...string) ([]string, error) {
	vars := [][]string{}
	for _, name := range profileNames {
		t := LookupProfileTarget(name, target)
		if t == nil {
			return nil, fmt.Errorf("failed to lookup %v --target=%v", name, target)
		}
		vars = append(vars, t.Env.Vars)
	}
	MergeEnv(concat, ignore, env, vars...)
	return env.ToSlice(), nil
}

// PrependToPath prepends its argument to the PATH environment variable.
func (ch *ConfigHelper) PrependToPATH(path string) {
	existing := ch.GetTokens("PATH", ":")
	ch.SetTokens("PATH", append([]string{path}, existing...), ":")
}

// SetGoPath computes and sets the GOPATH environment variable based on the
// current jiri configuration.
func (ch *ConfigHelper) SetGoPath() {
	if !ch.legacyMode {
		ch.pathHelper("GOPATH", ch.root, ch.projects, ch.config.GoWorkspaces(), "")
	}
}

// SetVDLPath computes and sets the VDLPATH environment variable based on the
// current jiri configuration.
func (ch *ConfigHelper) SetVDLPath() {
	if !ch.legacyMode {
		ch.pathHelper("VDLPATH", ch.root, ch.projects, ch.config.VDLWorkspaces(), "src")
	}
}

// pathHelper is a utility function for determining paths for project workspaces.
func (ch *ConfigHelper) pathHelper(name, root string, projects project.Projects, workspaces []string, suffix string) {
	path := ch.GetTokens(name, ":")
	for _, workspace := range workspaces {
		absWorkspace := filepath.Join(root, workspace, suffix)
		// Only append an entry to the path if the workspace is rooted
		// under a jiri project that exists locally or vice versa.
		for _, project := range projects {
			// We check if <project.Path> is a prefix of <absWorkspace> to
			// account for Go workspaces nested under a single jiri project,
			// such as: $JIRI_ROOT/release/projects/chat/go.
			//
			// We check if <absWorkspace> is a prefix of <project.Path> to
			// account for Go workspaces that span multiple jiri projects,
			// such as: $JIRI_ROOT/release/go.
			if strings.HasPrefix(absWorkspace, project.Path) || strings.HasPrefix(project.Path, absWorkspace) {
				if _, err := ch.ctx.Run().Stat(filepath.Join(absWorkspace)); err == nil {
					path = append(path, absWorkspace)
					break
				}
			}
		}
	}
	ch.SetTokens(name, path, ":")
}
