package server

import (
	"errors"
	"net/http"
	"strconv"
)

func parseOutputRequest(r *http.Request) (offset int64, full bool, err error) {
	query := r.URL.Query()
	if raw := query.Get("offset"); raw != "" {
		offset, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || offset < 0 {
			return 0, false, errors.New("invalid output offset")
		}
	}
	switch query.Get("full") {
	case "", "0":
	case "1":
		full = true
	default:
		return 0, false, errors.New("invalid full output mode")
	}
	if full && offset != 0 {
		return 0, false, errors.New("full output requires offset zero")
	}
	return offset, full, nil
}
