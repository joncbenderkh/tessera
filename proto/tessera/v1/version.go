// SPDX-License-Identifier: GPL-3.0-or-later

package tesserav1

// ProtocolVersion is the plugin protocol version: the PanelService RPCs
// and their messages. The transport handshake negotiates it, and the
// plugin repeats it in DescribeResponse.protocol_version.
const ProtocolVersion = 1

// ViewVersion is the newest view schema version this package describes.
// The host offers its supported versions in DescribeRequest; the plugin
// picks one in DescribeResponse.view_version.
const ViewVersion = 1

// The host sets MagicCookieKey=MagicCookieValue in a plugin's environment.
// A plugin started without it was run by hand and should exit with a
// message instead of waiting for a host. The cookie is a usability check,
// not a security measure.
const (
	MagicCookieKey   = "TESSERA_PLUGIN_MAGIC_COOKIE"
	MagicCookieValue = "6f1d0b5c3e2a4978b8c1d7e05a9f3c21"
)
