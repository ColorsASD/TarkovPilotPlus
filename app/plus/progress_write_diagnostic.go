package plus

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/ggdiam/TarkovPilot/app/plus/internal/market"
)

// Csak kategóriák és revízióegyezések kerülnek az átmeneti státuszba.
// Ismeretlen Error(), URL, választest és személyes azonosító nem jelenik meg.
func progressWriteDiagnostic(result market.ProgressWriteResult, writeErr error, beforeRevision, readRevision string, readErr error) string {
	failure := market.ProgressWriteFailure(writeErr)
	ok, changed := "unknown", "unknown"
	if writeErr == nil {
		ok, changed = strconv.FormatBool(result.OK), strconv.FormatBool(result.Changed)
	} else {
		if failure.ACKOK != nil {
			ok = strconv.FormatBool(*failure.ACKOK)
		}
		if failure.ACKChanged != nil {
			changed = strconv.FormatBool(*failure.ACKChanged)
		}
	}
	ackBefore, readACK := "unknown", "unknown"
	if result.Revision != "" && beforeRevision != "" {
		ackBefore = strconv.FormatBool(result.Revision == beforeRevision)
	}
	if readErr == nil && result.Revision != "" && readRevision != "" {
		readACK = strconv.FormatBool(readRevision == result.Revision)
	}
	readFailure := progressFailureLabel(market.ProgressWriteFailure(readErr))
	if readErr == nil && readRevision == "" {
		readFailure = "not_requested"
	}
	return fmt.Sprintf("API: ok=%s; changed=%s; ACK same as before=%s; GET matches ACK=%s; write=%s; read=%s.",
		ok, changed, ackBefore, readACK, progressFailureLabel(failure), readFailure)
}

func progressFailureLabel(info market.ProgressWriteFailureInfo) string {
	if info.HTTPStatus >= 100 && info.HTTPStatus <= 599 {
		return fmt.Sprintf("%s(HTTP %d)", info.Reason, info.HTTPStatus)
	}
	return info.Reason
}

func progressItemMismatch(selected market.Item, observed market.TrackedSnapshot) string {
	name, matches := "", 0
	for _, row := range observed.Items {
		if row.ItemID == selected.BSGID {
			name, matches = row.Name, matches+1
		}
	}
	if matches != 1 || name == "" || selected.Name == "" || strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(selected.Name)) {
		return ""
	}
	return fmt.Sprintf(" Selected item: %s; tracked item: %s.", diagnosticItemName(selected.Name), diagnosticItemName(name))
}

func diagnosticItemName(value string) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
	characters := []rune(strings.Join(strings.Fields(value), " "))
	if len(characters) > 60 {
		characters = append(characters[:59], '…')
	}
	return string(characters)
}
