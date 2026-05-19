package server

import (
	"fmt"
	"net/http"
	"strings"
)

func queryOrBodyID(r *http.Request, queryName string, bodyValues ...string) (string, error) {
	return queryOrBodyIDAny(r, []string{queryName}, bodyValues...)
}

func queryOrBodyIDAny(r *http.Request, queryNames []string, bodyValues ...string) (string, error) {
	queryValue := ""
	queryName := ""
	for _, name := range queryNames {
		if value := strings.TrimSpace(r.URL.Query().Get(name)); value != "" {
			if queryValue != "" && queryValue != value {
				return "", fmt.Errorf("%s mismatch", name)
			}
			queryValue = value
			queryName = name
		}
	}
	if queryName == "" && len(queryNames) > 0 {
		queryName = queryNames[0]
	}
	bodyValue := firstNonBlank(bodyValues...)
	if queryValue != "" && bodyValue != "" && queryValue != bodyValue {
		return "", fmt.Errorf("%s mismatch", queryName)
	}
	if queryValue != "" {
		return queryValue, nil
	}
	return bodyValue, nil
}
