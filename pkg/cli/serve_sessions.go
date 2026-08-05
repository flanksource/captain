package cli

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/timberio/go-datemath"
)

var (
	sessionRelativeDateMathPattern = regexp.MustCompile(`^now(?:[+-]\d*[yMwdhHms]|/[yMwdhHms])*$`)
	sessionAbsoluteDateMathPattern = regexp.MustCompile(`^[0-9][0-9T:Z.+-]*(?:\|\|(?:[+-]\d*[yMwdhHms]|/[yMwdhHms])*)?$`)
)

func handleSessionsLive() http.HandlerFunc {
	return handleSessionsLiveWithRunner(RunSessionLive)
}

func handleSessionsLiveWithRunner(run func(context.Context, SessionLiveOptions) (SessionLiveResult, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		opts := SessionLiveOptions{
			Source: strings.TrimSpace(query.Get("source")), Project: strings.TrimSpace(query.Get("project")),
			Query: strings.TrimSpace(query.Get("q")), Limit: 100, Cursor: strings.TrimSpace(query.Get("cursor")),
		}
		if opts.Source == "" {
			opts.Source = "all"
		}
		if raw := strings.TrimSpace(query.Get("all")); raw != "" {
			opts.All, _ = strconv.ParseBool(raw)
		}
		if strings.EqualFold(opts.Project, "all") {
			opts.Project, opts.All = "", true
		} else if opts.Project != "" {
			opts.All = false
		}
		if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
			limit, err := strconv.Atoi(raw)
			if err != nil {
				http.Error(w, fmt.Sprintf("invalid limit %q", raw), http.StatusBadRequest)
				return
			}
			opts.Limit = limit
		}
		var err error
		now := time.Now()
		if opts.From, err = parseSessionQueryTime(sessionQueryTimeOptions{
			Name: "from", Value: query.Get("from"), Now: now,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if opts.Before, err = parseSessionQueryTime(sessionQueryTimeOptions{
			Name: "before", Value: query.Get("before"), Now: now,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, _, err := sessionActivityRange(opts.From, opts.Before); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result, err := run(r.Context(), opts)
		if err != nil {
			http.Error(w, err.Error(), serveRunStatus(err, http.StatusBadRequest))
			return
		}
		writeServeJSON(w, http.StatusOK, result)
	}
}

type sessionQueryTimeOptions struct {
	Name  string
	Value string
	Now   time.Time
}

func parseSessionQueryTime(opts sessionQueryTimeOptions) (time.Time, error) {
	value := strings.TrimSpace(opts.Value)
	if value == "" {
		return time.Time{}, nil
	}

	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	if !sessionRelativeDateMathPattern.MatchString(value) && !sessionAbsoluteDateMathPattern.MatchString(value) {
		return time.Time{}, fmt.Errorf("invalid %s timestamp %q", opts.Name, value)
	}

	expression, err := datemath.Parse(value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s timestamp %q: %w", opts.Name, value, err)
	}
	return expression.Time(datemath.WithNow(opts.Now)), nil
}

func handleSessionsThroughput() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		opts := SessionThroughputOptions{
			Source: strings.TrimSpace(query.Get("source")), Project: strings.TrimSpace(query.Get("project")),
			Query: strings.TrimSpace(query.Get("q")), Limit: defaultSessionThroughputLimit,
		}
		if opts.Source == "" {
			opts.Source = "all"
		}
		if raw := strings.TrimSpace(query.Get("all")); raw != "" {
			opts.All, _ = strconv.ParseBool(raw)
		}
		if strings.EqualFold(opts.Project, "all") {
			opts.Project, opts.All = "", true
		} else if opts.Project != "" {
			opts.All = false
		}
		if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
			limit, err := strconv.Atoi(raw)
			if err != nil {
				http.Error(w, fmt.Sprintf("invalid limit %q", raw), http.StatusBadRequest)
				return
			}
			opts.Limit = limit
		}
		result, err := RunSessionThroughput(r.Context(), opts)
		if err != nil {
			http.Error(w, err.Error(), serveRunStatus(err, http.StatusBadRequest))
			return
		}
		writeServeJSON(w, http.StatusOK, result)
	}
}

func handleProjects() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := RunProjectOptions(r.Context())
		if err != nil {
			http.Error(w, err.Error(), serveRunStatus(err, http.StatusBadRequest))
			return
		}
		writeServeJSON(w, http.StatusOK, result)
	}
}

func handleSessionGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		if id == "" {
			http.Error(w, "session id is required", http.StatusBadRequest)
			return
		}
		query := r.URL.Query()
		s, err := RunSessionGet(r.Context(), SessionGetOptions{
			ID: id, Offset: queryInt(query.Get("offset")), Limit: queryInt(query.Get("limit")), Tail: queryInt(query.Get("tail")),
		})
		if err != nil {
			http.Error(w, err.Error(), serveRunStatus(err, http.StatusNotFound))
			return
		}
		writeServeJSON(w, http.StatusOK, s)
	}
}

func queryInt(value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
