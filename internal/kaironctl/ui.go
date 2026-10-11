// Copyright 2026 Zyvor · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package kaironctl

import (
	"os"
	"strings"
)

// uiBaseURL is the kairon-ui address from KAIRON_UI_URL, without a trailing
// slash; "" when unset. Every kaironctl verb that talks to kairon-ui (image,
// network observability, packet capture) resolves it here.
func uiBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("KAIRON_UI_URL")), "/")
}

// uiToken is the bearer token for kairon-ui: KAIRON_UI_TOKEN, and, when
// allowConsole is set, KAIRON_CONSOLE_TOKEN as a fallback (the observability
// pass-through historically accepted the console token; other routes do not).
func uiToken(allowConsole bool) string {
	if t := os.Getenv("KAIRON_UI_TOKEN"); t != "" {
		return t
	}
	if allowConsole {
		return os.Getenv("KAIRON_CONSOLE_TOKEN")
	}
	return ""
}
