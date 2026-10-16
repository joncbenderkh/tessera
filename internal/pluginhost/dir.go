// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/adrg/xdg"

	"github.com/joncbenderkh/tessera/internal/config"
)

// DefaultDir is the plugins directory: tessera/plugins under the user data
// directory ($XDG_DATA_HOME, or ~/.local/share, on Linux).
func DefaultDir() string {
	return filepath.Join(xdg.DataHome, "tessera", "plugins")
}

// exeSuffix is what an executable's file name adds to its plugin name.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// List returns the names of the plugins installed in dir, sorted. Files
// that are not executables named by the plugin-name grammar, such as
// clock.py beside clock, are ignored. A missing dir has no plugins.
func List(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		name, ok := cutExeSuffix(e.Name())
		if !ok || config.ValidPluginName(name) != nil {
			continue
		}
		if _, err := find(dir, name); err == nil {
			names = append(names, name)
		}
	}
	return names, nil
}

// cutExeSuffix strips exeSuffix from a file name, ignoring case on
// Windows, whose file names are case-insensitive.
func cutExeSuffix(file string) (string, bool) {
	suffix := exeSuffix()
	if len(file) < len(suffix) || !strings.EqualFold(file[len(file)-len(suffix):], suffix) {
		return file, false
	}
	return file[:len(file)-len(suffix)], true
}

// find returns the path of the plugin called name in dir, or an error
// saying why it cannot be run. Those that name the file are permanent,
// as restarting does not change it; one that stat itself returns is
// not.
func find(dir, name string) (string, error) {
	if err := config.ValidPluginName(name); err != nil {
		return "", permanent(err)
	}
	path := filepath.Join(dir, name+exeSuffix())
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", permanent(fmt.Errorf("plugin %q is not installed in %s", name, dir))
	}
	if err != nil {
		return "", err // such as EIO, or a mount not ready yet: worth retrying
	}
	if !info.Mode().IsRegular() {
		return "", permanent(fmt.Errorf("plugin %q: %s is not a regular file", name, path))
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", permanent(fmt.Errorf("plugin %q: %s is not executable", name, path))
	}
	return path, nil
}
