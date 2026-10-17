// SPDX-License-Identifier: GPL-3.0-or-later

package pluginhost

import "errors"

// permanentError marks a launch error that restarting the plugin cannot
// fix: it is missing, misnamed, speaks another version, or rejects its
// settings.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func permanent(err error) error { return permanentError{err} }

// execError marks the error of starting the plugin's process, as
// against one from go-plugin's setup around it, such as its socket
// directory.
type execError struct{ error }

func (e execError) Unwrap() error { return e.error }

// hangError marks a call the plugin did not answer in time; the
// plugin is likely still running.
type hangError struct{ error }

func (e hangError) Unwrap() error { return e.error }

// Retryable reports whether restarting the plugin could fix err.
func Retryable(err error) bool {
	var p permanentError
	return !errors.As(err, &p)
}
