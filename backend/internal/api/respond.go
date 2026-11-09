package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// apiError 是统一的业务错误，带 HTTP 状态码。
type apiError struct {
	Status int    `json:"-"`
	Code   string `json:"code"`
	Msg    string `json:"error"`
}

func (e *apiError) Error() string { return e.Msg }

func badRequest(msg string) *apiError   { return &apiError{Status: 400, Code: "bad_request", Msg: msg} }
func unauthorized(msg string) *apiError { return &apiError{Status: 401, Code: "unauthorized", Msg: msg} }
func forbidden(msg string) *apiError    { return &apiError{Status: 403, Code: "forbidden", Msg: msg} }
func notFound(msg string) *apiError     { return &apiError{Status: 404, Code: "not_found", Msg: msg} }
func conflict(msg string) *apiError     { return &apiError{Status: 409, Code: "conflict", Msg: msg} }
func serverError(msg string) *apiError  { return &apiError{Status: 500, Code: "server_error", Msg: msg} }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func ok(w http.ResponseWriter, v any) { writeJSON(w, http.StatusOK, v) }

func fail(w http.ResponseWriter, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		writeJSON(w, ae.Status, ae)
		return
	}
	writeJSON(w, http.StatusInternalServerError, &apiError{Status: 500, Code: "server_error", Msg: err.Error()})
}

// decodeBody 限制请求体大小并严格解析 JSON。
func decodeBody(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return badRequest("请求内容解析失败: " + err.Error())
	}
	return nil
}

func pathInt64(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	if raw == "" {
		return 0, badRequest("缺少参数 " + name)
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, badRequest("参数 " + name + " 必须是数字")
	}
	return v, nil
}

func queryInt(r *http.Request, name string, def int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

func queryBool(r *http.Request, name string, def bool) bool {
	raw := strings.ToLower(strings.TrimSpace(r.URL.Query().Get(name)))
	if raw == "" {
		return def
	}
	return raw == "1" || raw == "true" || raw == "yes"
}

func queryInt64(r *http.Request, name string, def int64) int64 {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return def
	}
	return v
}

func nowMS() int64 { return time.Now().UnixMilli() }
