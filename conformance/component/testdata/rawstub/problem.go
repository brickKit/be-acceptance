package main

import (
	"errors"
	"fmt"
	"strings"
)

// apiError is the one error object of P4: a canonical code, a reason, its domain and string
// metadata. cause is the original error, which goes only to the log.
type apiError struct {
	Code       string
	Reason     string
	Domain     string
	Metadata   map[string]string
	Violations []violation
	cause      error
	header     map[string]string
}

type violation struct {
	Field       string `json:"field"`
	Reason      string `json:"reason"`
	Description string `json:"description,omitempty"`
}

func (e *apiError) Error() string {
	if e.cause != nil {
		return e.Reason + ": " + e.cause.Error()
	}
	return e.Reason
}

func (e *apiError) Unwrap() error { return e.cause }

// beErr builds an error of a reserved reason of domain be.
func beErr(reason string, meta map[string]string) *apiError {
	ent := beReasons[reason]
	return &apiError{Code: ent.code, Reason: reason, Domain: "be", Metadata: meta}
}

// ownErr builds an error of this component's own catalogue.
func ownErr(reason string, meta map[string]string) *apiError {
	ent := ownReasons[reason]
	return &apiError{Code: ent.code, Reason: reason, Domain: componentID, Metadata: meta}
}

// internalErr hides cause behind the generic INTERNAL answer (P4.3).
func internalErr(cause error) *apiError {
	return &apiError{Code: "INTERNAL", Reason: "INTERNAL", Domain: "be", cause: cause}
}

// asAPIError turns any error into an apiError: unclassified errors become INTERNAL.
func asAPIError(err error) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	return internalErr(err)
}

// normalized applies P4.3: INTERNAL, UNKNOWN, DATA_LOSS, an error without a code and an error
// with a reason but no domain answer reason INTERNAL, domain be, empty metadata.
func (e *apiError) normalized() *apiError {
	code := e.Code
	if _, ok := grpcCodes[code]; !ok || code == "OK" {
		code = "INTERNAL"
	}
	if code == "INTERNAL" || code == "UNKNOWN" || code == "DATA_LOSS" || e.Reason == "" || e.Domain == "" {
		if code != "UNKNOWN" && code != "DATA_LOSS" {
			code = "INTERNAL"
		}
		return &apiError{Code: code, Reason: "INTERNAL", Domain: "be", Metadata: map[string]string{}, cause: e}
	}
	out := *e
	out.Code = code
	if out.Metadata == nil {
		out.Metadata = map[string]string{}
	}
	return &out
}

// httpStatus is the code table plus the BODY_TOO_LARGE exception.
func (e *apiError) httpStatus() int {
	if e.Domain == "be" && e.Reason == "BODY_TOO_LARGE" {
		return 413
	}
	return grpcCodes[e.Code].http
}

// texts returns the title and the rendered detail in the default locale.
func (e *apiError) texts(locale string) (string, string) {
	var ent reasonEntry
	var ok bool
	if e.Domain == "be" {
		ent, ok = beReasons[e.Reason]
	} else if e.Domain == componentID {
		ent, ok = ownReasons[e.Reason]
	}
	if !ok {
		ent = beReasons["INTERNAL"]
	}
	title, msg := ent.titleEn, ent.messageEn
	if langOf(locale) == "zh" {
		title, msg = ent.titleZh, ent.msgZh
	}
	for k, v := range e.Metadata {
		msg = strings.ReplaceAll(msg, "{"+k+"}", v)
	}
	return title, msg
}

// langOf maps a BCP 47 tag to a catalogue language: zh for any zh tag, en otherwise.
func langOf(locale string) string {
	if strings.EqualFold(strings.SplitN(locale, "-", 2)[0], "zh") {
		return "zh"
	}
	return "en"
}

// problemBody renders the RFC 9457 body of P4.1. leak is the broken variant leak-internal.
func problemBody(e *apiError, locale, path, requestID, traceID string, leak bool) map[string]any {
	n := e.normalized()
	title, detail := n.texts(locale)
	if leak && n.Reason == "INTERNAL" && n.cause != nil {
		detail = rootCause(n.cause).Error()
	}
	meta := map[string]any{}
	for k, v := range n.Metadata {
		meta[k] = v
	}
	body := map[string]any{
		"type":       fmt.Sprintf("urn:be:%s:%s", n.Domain, n.Reason),
		"title":      title,
		"status":     n.httpStatus(),
		"code":       n.Code,
		"reason":     n.Reason,
		"domain":     n.Domain,
		"detail":     detail,
		"metadata":   meta,
		"instance":   path,
		"request_id": requestID,
		"trace_id":   traceID,
	}
	if len(n.Violations) > 0 {
		body["violations"] = n.Violations
	}
	return body
}

// rootCause unwraps to the innermost non-apiError cause, for the log and the leak variant.
func rootCause(err error) error {
	for {
		ae, ok := err.(*apiError)
		if !ok || ae.cause == nil {
			return err
		}
		err = ae.cause
	}
}
