package market

import (
	"context"
	"errors"
	"fmt"
)

// ProgressWriteFailureInfo csak ellenőrzött, megjeleníthető diagnosztikai adat.
// Nem tartalmaz URL-t, kulcsot, választestet vagy revíziót.
type ProgressWriteFailureInfo struct {
	Reason     string
	HTTPStatus int
	ACKOK      *bool
	ACKChanged *bool
}

type progressDiagnosticError struct {
	details ProgressWriteFailureInfo
	cause   error
}

func (e *progressDiagnosticError) Error() string {
	if e.details.HTTPStatus != 0 {
		return fmt.Sprintf("Tarkov Market API response: HTTP %d", e.details.HTTPStatus)
	}
	return "Tarkov Market progress response: " + e.details.Reason
}

func (e *progressDiagnosticError) Unwrap() error { return e.cause }

// ProgressWriteFailure szándékosan nem olvassa az ismeretlen hiba szövegét.
// Az errors.Is kategóriák és a kliens saját typed hibái az egyetlen források.
func ProgressWriteFailure(err error) ProgressWriteFailureInfo {
	if err == nil {
		return ProgressWriteFailureInfo{Reason: "none"}
	}
	var controlled *progressDiagnosticError
	if errors.As(err, &controlled) {
		return controlled.details
	}
	reason := "unknown"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		reason = "timeout"
	case errors.Is(err, context.Canceled):
		reason = "canceled"
	case errors.Is(err, ErrProgressUnavailable):
		reason = "unavailable"
	case errors.Is(err, ErrProgressWriteUncertain):
		reason = "uncertain"
	case errors.Is(err, ErrRateLimited):
		reason = "rate_limited"
	case errors.Is(err, ErrUnauthorized), errors.Is(err, ErrAPIKeyRequired):
		reason = "unauthorized"
	case errors.Is(err, ErrProgressForbidden):
		reason = "forbidden"
	case errors.Is(err, ErrProgressConflict):
		reason = "conflict"
	case errors.Is(err, ErrProgressNotFound):
		reason = "not_found"
	case errors.Is(err, ErrInvalidProgress):
		reason = "invalid_response"
	case errors.Is(err, ErrResponseTooBig):
		reason = "response_too_large"
	case errors.Is(err, ErrProgressInvalidQuantity), errors.Is(err, ErrProgressInvalidRequest), errors.Is(err, ErrProgressRevisionRequired):
		reason = "invalid_request"
	}
	return ProgressWriteFailureInfo{Reason: reason}
}

func progressHTTPError(status int, cause error) error {
	if status < 100 || status > 599 {
		return &progressDiagnosticError{details: ProgressWriteFailureInfo{Reason: "invalid_http_status"}, cause: cause}
	}
	return &progressDiagnosticError{details: ProgressWriteFailureInfo{Reason: "http", HTTPStatus: status}, cause: cause}
}

func invalidWriteResponse(reason string, ok, changed *bool) error {
	return errors.Join(ErrProgressWriteUncertain, &progressDiagnosticError{
		details: ProgressWriteFailureInfo{Reason: reason, ACKOK: ok, ACKChanged: changed}, cause: ErrInvalidProgress,
	})
}
