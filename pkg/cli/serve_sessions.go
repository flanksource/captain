package cli

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func handleSessionsLive() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		opts := SessionLiveOptions{
			Source: strings.TrimSpace(query.Get("source")), Project: strings.TrimSpace(query.Get("project")),
			Query: strings.TrimSpace(query.Get("q")), Limit: 100,
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
		result, err := RunSessionLive(r.Context(), opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeServeJSON(w, http.StatusOK, result)
	}
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
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeServeJSON(w, http.StatusOK, result)
	}
}

func handleProjects() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := RunProjectOptions(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
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
			http.Error(w, err.Error(), http.StatusNotFound)
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
